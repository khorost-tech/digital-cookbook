// Command consumer-groups — стенд #2 серии "Kafka: глубокое погружение":
// consumer groups, стратегии назначения партиций, ребалансировка, static
// membership, commit offset (auto vs manual). Тот же сценарий воспроизведён
// на Java (../../java/consumer-groups) — см. ../../README.md.
//
// Запуск (из контейнера на сети kafka-cookbook-net):
//
//	docker run --rm --network kafka-cookbook-net -v "$(pwd)/go:/app" -w /app golang:1.25 \
//	  go run ./consumer-groups -brokers=kafka1:9092,kafka2:9092,kafka3:9092 -scenario=all
//
// common.go: топик, общая инфраструктура (member — обёртка над kgo.Client с
// хуками ребаланса, continuousProducer, ассерты).
package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	topic       = "demo-groups"
	partitions  = 6
	replication = 3
)

var seeds []string

// ensureTopic идемпотентно (пере)создаёт топик demo-groups — общий для всех
// четырёх сценариев (у каждого сценария свой group.id, поэтому они друг
// другу не мешают, а данные топика переиспользуются).
func ensureTopic(brokers []string) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		log.Fatalf("kgo.NewClient (admin): %v", err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := adm.DeleteTopics(ctx, topic); err != nil {
		log.Fatalf("DeleteTopics: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		listed, err := adm.ListTopics(ctx, topic)
		if err == nil {
			if t, ok := listed[topic]; !ok || t.Err != nil {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	resp, err := adm.CreateTopics(ctx, partitions, replication, nil, topic)
	if err != nil {
		log.Fatalf("CreateTopics: %v", err)
	}
	for _, t := range resp {
		if t.Err != nil {
			log.Fatalf("CreateTopics %s: %v", t.Topic, t.Err)
		}
		fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d)\n", t.Topic, partitions, replication)
	}
}

// ---- лог с меткой времени от старта сценария (упорядочивает события
// ребаланса из разных горутин-консьюмеров в читаемую хронологию) ----

var (
	logMu sync.Mutex
	logT0 time.Time
)

func startClock() { logT0 = time.Now() }

func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	elapsed := time.Since(logT0).Round(time.Millisecond)
	fmt.Printf("[%8s] %s\n", elapsed.String(), fmt.Sprintf(format, args...))
}

// ---- partitionSet: множество номеров партиций, для сравнения назначений ----

type partitionSet map[int32]bool

func setFromAssigned(m map[string][]int32) partitionSet {
	ps := partitionSet{}
	for _, parts := range m {
		for _, p := range parts {
			ps[p] = true
		}
	}
	return ps
}

func (ps partitionSet) slice() []int32 {
	xs := make([]int32, 0, len(ps))
	for p := range ps {
		xs = append(xs, p)
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return xs
}

func (ps partitionSet) String() string {
	xs := ps.slice()
	strs := make([]string, len(xs))
	for i, x := range xs {
		strs[i] = fmt.Sprintf("%d", x)
	}
	return "[" + strings.Join(strs, " ") + "]"
}

// revokeEvent — одно REVOKED-событие вместе с классификацией: full==true
// значит у члена отозвали ВСЮ его текущую партицию (stop-the-world, eager);
// full==false при непустом ps значит отозвали ЧАСТЬ (инкрементально,
// cooperative) — отличать по одному лишь "ps пуст/не пуст" НЕЛЬЗЯ, потому
// что cooperative-sticky может отозвать непустое, но НЕ полное множество
// (см. README, реальный прогон: revoked [1] у члена, у которого было [0 1]
// — это НЕ stop-the-world, member сохранил партицию 0).
type revokeEvent struct {
	ps   partitionSet
	full bool
}

func equalSets(a, b partitionSet) bool {
	if len(a) != len(b) {
		return false
	}
	for p := range a {
		if !b[p] {
			return false
		}
	}
	return true
}

// ---- member: обёртка над kgo.Client — один участник consumer group ----

type member struct {
	id string
	cl *kgo.Client

	mu           sync.Mutex
	assigned     partitionSet
	assignEvents []partitionSet // история непустых ASSIGNED-событий
	revokeEvents []revokeEvent  // история REVOKED-событий (в т.ч. пустые — важно для cooperative)
	lostEvents   []partitionSet

	firstAssignOnce sync.Once
	firstAssign     chan struct{}

	consumed atomic.Int64

	process func(m *member, fetches kgo.Fetches)

	stop chan struct{}
	done chan struct{}
}

func defaultProcess(m *member, fetches kgo.Fetches) {
	fetches.EachRecord(func(r *kgo.Record) {
		m.consumed.Add(1)
	})
}

// newMember создаёт нового участника группы groupID. process==nil -> просто
// считает полученные записи (defaultProcess). extraOpts — дополнительные
// GroupOpt/ClientOpt (стратегия балансировки, instance-id, session timeout,
// autocommit-настройки и т.д.).
func newMember(id, groupID string, process func(m *member, fetches kgo.Fetches), extraOpts ...kgo.Opt) *member {
	m := &member{
		id:          id,
		assigned:    partitionSet{},
		firstAssign: make(chan struct{}),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
		process:     process,
	}
	if m.process == nil {
		m.process = defaultProcess
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(seeds...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(groupID),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
		kgo.ClientID(id),
		kgo.OnPartitionsAssigned(func(_ context.Context, _ *kgo.Client, assigned map[string][]int32) {
			ps := setFromAssigned(assigned)
			m.mu.Lock()
			for p := range ps {
				m.assigned[p] = true
			}
			m.assignEvents = append(m.assignEvents, ps)
			cur := m.assigned.String()
			m.mu.Unlock()
			logf("%-22s ASSIGNED %-14v текущее назначение: %s", id, ps, cur)
			m.firstAssignOnce.Do(func() { close(m.firstAssign) })
		}),
		kgo.OnPartitionsRevoked(func(_ context.Context, _ *kgo.Client, revoked map[string][]int32) {
			ps := setFromAssigned(revoked)
			m.mu.Lock()
			priorSize := len(m.assigned)
			for p := range ps {
				delete(m.assigned, p)
			}
			full := priorSize > 0 && len(ps) == priorSize
			m.revokeEvents = append(m.revokeEvents, revokeEvent{ps: ps, full: full})
			cur := m.assigned.String()
			m.mu.Unlock()
			if len(ps) == 0 {
				logf("%-22s REVOKED  %-14v (пусто — инкрементальная ребалансировка ничего не забрала) текущее: %s", id, ps, cur)
			} else {
				logf("%-22s REVOKED  %-14v текущее назначение: %s", id, ps, cur)
			}
		}),
		kgo.OnPartitionsLost(func(_ context.Context, _ *kgo.Client, lost map[string][]int32) {
			ps := setFromAssigned(lost)
			m.mu.Lock()
			for p := range ps {
				delete(m.assigned, p)
			}
			m.lostEvents = append(m.lostEvents, ps)
			m.mu.Unlock()
			logf("%-22s LOST     %v (сессия истекла до ре-джойна)", id, ps)
		}),
	}
	opts = append(opts, extraOpts...)

	cl, err := kgo.NewClient(opts...)
	if err != nil {
		log.Fatalf("newMember %s: %v", id, err)
	}
	m.cl = cl
	go m.pollLoop()
	return m
}

func (m *member) pollLoop() {
	defer close(m.done)
	for {
		select {
		case <-m.stop:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		fetches := m.cl.PollFetches(ctx)
		cancel()
		for _, e := range fetches.Errors() {
			if e.Err == context.DeadlineExceeded || e.Err == context.Canceled {
				continue
			}
			log.Printf("%s: fetch error topic=%s partition=%d: %v", m.id, e.Topic, e.Partition, e.Err)
		}
		m.process(m, fetches)
	}
}

func (m *member) waitFirstAssign(timeout time.Duration) {
	select {
	case <-m.firstAssign:
	case <-time.After(timeout):
		log.Fatalf("%s: не получил начальное назначение партиций за %s", m.id, timeout)
	}
}

func (m *member) snapshot() partitionSet {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := partitionSet{}
	for p := range m.assigned {
		cp[p] = true
	}
	return cp
}

func (m *member) eventCounts() (assigns, revokes, losts int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.assignEvents), len(m.revokeEvents), len(m.lostEvents)
}

func (m *member) revokeHistory() []revokeEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]revokeEvent(nil), m.revokeEvents...)
}

// close корректно останавливает поллинг и закрывает клиента (graceful —
// для динамических членов это шлёт LeaveGroupRequest, для статических
// (InstanceID) — НЕТ, см. content-note про static membership в README).
func (m *member) close() {
	select {
	case <-m.stop:
		// уже закрыт
	default:
		close(m.stop)
	}
	<-m.done
	m.cl.Close()
}

// waitUntilStable ждёт, пока объединение назначений members не покроет все
// partitions без пересечений, или до истечения timeout. Кооперативный
// ребаланс (cooperative-sticky) может сходиться НЕСКОЛЬКИМИ раундами
// JoinGroup/SyncGroup (сначала часть партиций "повисает" ничьей, пока
// прежний владелец не отзовёт их явно) — поэтому фиксированная пауза перед
// assertPartitioned ненадёжна, реально ждём сходимости.
func waitUntilStable(timeout time.Duration, members ...*member) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		seen := map[int32]bool{}
		total := 0
		overlap := false
		for _, m := range members {
			for p := range m.snapshot() {
				if seen[p] {
					overlap = true
				}
				seen[p] = true
				total++
			}
		}
		if !overlap && len(seen) == partitions {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// assertPartitioned падает, если партиции пересекаются между членами или
// покрыты не полностью (0..partitions-1 ровно один раз суммарно).
func assertPartitioned(label string, members ...*member) {
	seen := map[int32]string{}
	total := partitionSet{}
	for _, m := range members {
		ps := m.snapshot()
		for p := range ps {
			if owner, ok := seen[p]; ok {
				log.Fatalf("[assert] FAIL (%s): партиция %d одновременно у %s и %s", label, p, owner, m.id)
			}
			seen[p] = m.id
			total[p] = true
		}
	}
	if len(total) != partitions {
		log.Fatalf("[assert] FAIL (%s): покрыто %d партиций из %d: %v", label, len(total), partitions, total)
	}
	logf("[assert] OK (%s): все %d партиций покрыты ровно одним консьюмером, пересечений нет", label, partitions)
}

// continuousProducer шлёт сообщения в topic, пока не закрыт stop, чередуя
// ключи part-0..part-{partitions-1} (гарантированно засевает все партиции).
// Продюсинг во время работы консьюмеров — осознанный выбор: если продюсить
// ТОЛЬКО до старта второго консьюмера, первый успевает дочитать всё до
// подключения второго, и живое перераспределение чтения между консьюмерами
// не видно в логе (см. README, content-note, урок из java-deep-dive/messaging).
func continuousProducer(cl *kgo.Client, stop <-chan struct{}, interval time.Duration) *atomic.Int64 {
	sent := &atomic.Int64{}
	go func() {
		i := 0
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				key := fmt.Sprintf("part-%d", i%partitions)
				val := fmt.Sprintf("live-%d", i)
				i++
				rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: []byte(val)}
				cl.Produce(context.Background(), rec, func(_ *kgo.Record, err error) {
					if err != nil {
						log.Printf("continuousProducer: produce error: %v", err)
						return
					}
					sent.Add(1)
				})
			}
		}
	}()
	return sent
}
