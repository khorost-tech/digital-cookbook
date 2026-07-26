package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// newAdm открывает лёгкое kadm-соединение для админ-операций (создание/удаление
// топиков, describe партиций). Закрывать через adm.Close() (закрывает
// внутренний kgo.Client).
func newAdm(seeds []string) *kadm.Client {
	cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatalf("kgo.NewClient (admin): %v", err)
	}
	return kadm.NewClient(cl)
}

// recreateTopic идемпотентно (пере)создаёт топик с заданными partitions/rf и
// конфигами (например min.insync.replicas, unclean.leader.election.enable) —
// тот же паттерн, что в log-basics/consumer-groups: удалить если есть,
// подождать пока пропадёт из метаданных, создать заново. Так каждый прогон
// стенда стартует с чистого состояния (офсеты с нуля).
func recreateTopic(seeds []string, name string, partitions int32, rf int16, configs map[string]*string) {
	adm := newAdm(seeds)
	defer adm.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := adm.DeleteTopics(ctx, name); err != nil {
		log.Fatalf("DeleteTopics %s: %v", name, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		listed, err := adm.ListTopics(ctx, name)
		if err == nil {
			if t, ok := listed[name]; !ok || t.Err != nil {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}

	resp, err := adm.CreateTopics(ctx, partitions, rf, configs, name)
	if err != nil {
		log.Fatalf("CreateTopics %s: %v", name, err)
	}
	for _, t := range resp {
		if t.Err != nil {
			log.Fatalf("CreateTopics %s: %v", t.Topic, t.Err)
		}
	}
	fmt.Printf("[admin] топик %s создан (partitions=%d, rf=%d, configs=%v)\n", name, partitions, rf, flattenConfigs(configs))
}

func flattenConfigs(configs map[string]*string) map[string]string {
	out := map[string]string{}
	for k, v := range configs {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// alterTopicConfig меняет конфиг существующего топика (используется для
// unclean.leader.election.enable — переключение "на лету" без пересоздания
// топика, чтобы не терять уже записанные данные и лаг реплик).
func alterTopicConfig(seeds []string, name, key, value string) {
	adm := newAdm(seeds)
	defer adm.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := value
	_, err := adm.AlterTopicConfigs(ctx, []kadm.AlterConfig{{Op: kadm.SetConfig, Name: key, Value: &v}}, name)
	if err != nil {
		log.Fatalf("AlterTopicConfigs %s %s=%s: %v", name, key, value, err)
	}
	fmt.Printf("[admin] топик %s: %s=%s\n", name, key, value)
}

// describePartition возвращает детали партиции 0 топика name — падает, если
// топик/партиция не найдены (это баг стенда/кластера, не штатный исход).
func describePartition(seeds []string, name string) kadm.PartitionDetail {
	p, err := describePartitionSoft(seeds, name)
	if err != nil {
		log.Fatalf("describePartition %s: %v", name, err)
	}
	return p
}

// describePartitionSoft — как describePartition, но возвращает ошибку вместо
// Fatalf. Нужна для waitForLeader: сразу после CreateTopics метаданные о
// новом топике ещё могут не разойтись по кластеру (UNKNOWN_TOPIC_OR_PARTITION
// в первые сотни миллисекунд — это штатная гонка, не ошибка), поэтому вызов
// требующий retry не должен падать на первой неудаче.
func describePartitionSoft(seeds []string, name string) (kadm.PartitionDetail, error) {
	adm := newAdm(seeds)
	defer adm.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	details, err := adm.ListTopics(ctx, name)
	if err != nil {
		return kadm.PartitionDetail{}, fmt.Errorf("ListTopics: %w", err)
	}
	t, ok := details[name]
	if !ok || t.Err != nil {
		return kadm.PartitionDetail{}, fmt.Errorf("топик не найден или ошибка: %v", t.Err)
	}
	p, ok := t.Partitions[0]
	if !ok {
		return kadm.PartitionDetail{}, fmt.Errorf("партиция 0 не найдена")
	}
	return p, nil
}

// printPartitionState печатает лидера/ISR/реплики партиции 0 в человекочитаемом
// виде + пояснением какому compose-контейнеру соответствует node.id — эта
// строка и есть тот самый "лидер до/после kill", который фиксируется в отчёте.
func printPartitionState(label string, p kadm.PartitionDetail) {
	fmt.Printf("[describe] %s: topic=%s partition=%d leader=%d(%s) replicas=%v isr=%v offline=%v\n",
		label, p.Topic, p.Partition, p.Leader, containerFor(p.Leader), p.Replicas, p.ISR, p.OfflineReplicas)
}

// containerFor — node.id брокера → имя compose-контейнера (см. ../../compose/compose.yml).
// Чисто информационное сопоставление для логов; сам docker stop/start/kill
// выполняет оркестрирующий bash-скрипт (../../ops/broker-kill.sh) с хоста —
// у клиента внутри контейнера нет доступа к docker socket.
func containerFor(nodeID int32) string {
	switch nodeID {
	case 1:
		return "kafka-cookbook-1"
	case 2:
		return "kafka-cookbook-2"
	case 3:
		return "kafka-cookbook-3"
	default:
		return "unknown"
	}
}

func strPtr(s string) *string { return &s }

// waitForLeader ждёт, пока у партиции появится валидный (>=0) лидер —
// используется после kill/start брокера, чтобы дать контроллеру время
// провести переизбрание, прежде чем пробовать продюсить/консьюмить.
func waitForLeader(seeds []string, name string, timeout time.Duration) kadm.PartitionDetail {
	deadline := time.Now().Add(timeout)
	var last kadm.PartitionDetail
	var lastErr error
	for time.Now().Before(deadline) {
		p, err := describePartitionSoft(seeds, name)
		lastErr = err
		if err == nil {
			last = p
			if last.Leader >= 0 && last.Err == nil {
				return last
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Fatalf("waitForLeader: топик %s не получил лидера за %s (последнее состояние: leader=%d err=%v lastErr=%v)", name, timeout, last.Leader, last.Err, lastErr)
	return last
}

func joinInts(xs []int32) string {
	ss := make([]string, len(xs))
	for i, x := range xs {
		ss[i] = fmt.Sprintf("%d", x)
	}
	sort.Strings(ss)
	return strings.Join(ss, ",")
}
