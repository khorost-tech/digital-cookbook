// Демо к статье «Backpressure и load shedding» (khorost.tech).
//
// Нагрузчик: генератор производит элементы быстрее, чем потребитель успевает их
// обрабатывать. Сравниваются два приёмника при одинаковом входном потоке:
//
//   - unbounded — очередь без ограничения (растущий слайс). Никого не отклоняет,
//     но backlog и латентность обработки растут неограниченно: элемент ждёт в
//     очереди тем дольше, чем длиннее хвост.
//   - bounded+shed — ограниченная очередь (boundedqueue) с политикой Reject.
//     Держит короткую очередь и низкую латентность ценой отказов под перегрузом.
//
// Все числа считаются в рантайме и печатаются: принято / отброшено, пиковая
// длина очереди, p50/p99 латентности обработки (по собственным замерам «вошёл в
// очередь → начал обрабатываться»). Абсолютные значения зависят от машины —
// снимайте их своим прогоном.
//
// Запуск:
//
//	go run ./cmd/unbounded-vs-bounded
package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"tech.khorost/backpressure-cookbook/boundedqueue"
)

const (
	totalItems   = 1000                 // сколько элементов сгенерировать
	consumerCost = 3 * time.Millisecond // время обработки одного элемента потребителем
	producerCost = 1 * time.Millisecond // интервал между генерациями (генератор ~3x быстрее)
	queueCap     = 64                   // ёмкость ограниченной очереди
)

// item несёт момент постановки в очередь, чтобы замерить латентность ожидания.
type item struct {
	enqueued time.Time
}

// result — собранные метрики одного сценария.
type result struct {
	name      string
	accepted  int64
	dropped   int64
	peakQueue int
	latencies []time.Duration
}

func main() {
	fmt.Printf("нагрузка: %d элементов, обработка ~%v/шт, ёмкость bounded=%d\n\n",
		totalItems, consumerCost, queueCap)

	unb := runUnbounded()
	bnd := runBounded()

	printReport(unb)
	printReport(bnd)

	fmt.Println("\nВывод: unbounded принимает всё, но латентность обработки растёт")
	fmt.Println("вместе с backlog; bounded+shed держит очередь короткой и латентность")
	fmt.Println("низкой, отклоняя избыток. Числа у себя — они зависят от машины.")
}

// runUnbounded моделирует неограниченную очередь: генератор кладёт все элементы
// без отказов, потребитель разбирает их по одному. Пиковая длина очереди =
// максимальный наблюдённый backlog.
func runUnbounded() result {
	q := newUnboundedQueue()
	res := result{name: "unbounded", latencies: make([]time.Duration, 0, totalItems)}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // потребитель
		defer wg.Done()
		for {
			it, ok, closed := q.take()
			if closed {
				return
			}
			if !ok {
				continue
			}
			res.latencies = append(res.latencies, time.Since(it.enqueued))
			busy(consumerCost)
		}
	}()

	// Генератор: производит элементы быстрее потребителя (интервал producerCost).
	for i := 0; i < totalItems; i++ {
		q.push(item{enqueued: time.Now()})
		res.accepted++
		busy(producerCost)
	}
	res.peakQueue = q.peak()
	q.close()
	wg.Wait()
	return res
}

// runBounded использует ограниченную очередь с политикой Reject: избыток
// отклоняется, принятые обрабатываются с коротким ожиданием.
func runBounded() result {
	produceCtx := context.Background()
	consumeCtx, stopConsumer := context.WithCancel(context.Background())
	q := boundedqueue.New[item](queueCap, boundedqueue.Reject)
	res := result{name: fmt.Sprintf("bounded+shed(cap=%d)", queueCap),
		latencies: make([]time.Duration, 0, totalItems)}

	var peak int

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // потребитель: блокирующий Take, выход по отмене consumeCtx
		defer wg.Done()
		for {
			it, ok := q.Take(consumeCtx)
			if !ok {
				return
			}
			res.latencies = append(res.latencies, time.Since(it.enqueued))
			busy(consumerCost)
		}
	}()

	for i := 0; i < totalItems; i++ {
		if q.Offer(produceCtx, item{enqueued: time.Now()}) {
			if l := q.Len(); l > peak {
				peak = l // фиксируем пиковую длину ограниченной очереди
			}
		}
		busy(producerCost)
	}
	// Ждём, пока потребитель разберёт очередь, затем отменяем его контекст,
	// чтобы блокирующий Take вернул ok=false и горутина завершилась.
	for q.Len() > 0 {
		busy(consumerCost)
	}
	stopConsumer()
	wg.Wait()

	res.accepted = q.Accepted()
	res.dropped = q.Dropped()
	res.peakQueue = peak
	return res
}

// busy имитирует полезную работу фиксированной длительности активным ожиданием,
// чтобы не зависеть от разрешения планировщика на коротких интервалах.
func busy(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
	}
}

func printReport(r result) {
	p50 := percentile(r.latencies, 50)
	p99 := percentile(r.latencies, 99)
	fmt.Printf("%-24s принято=%-6d отброшено=%-6d пик очереди=%-6d p50=%-10v p99=%v\n",
		r.name, r.accepted, r.dropped, r.peakQueue, round(p50), round(p99))
}

func percentile(xs []time.Duration, p int) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	s := make([]time.Duration, len(xs))
	copy(s, xs)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (p * (len(s) - 1)) / 100
	return s[idx]
}

func round(d time.Duration) time.Duration {
	if d < time.Microsecond {
		return d
	}
	if d < time.Millisecond {
		return d.Round(time.Microsecond)
	}
	return d.Round(10 * time.Microsecond)
}

// --- неограниченная очередь (растущий слайс под мьютексом) ---

type unboundedQueue struct {
	mu     sync.Mutex
	items  []item
	closed bool
	peakN  int
}

func newUnboundedQueue() *unboundedQueue { return &unboundedQueue{} }

func (q *unboundedQueue) push(it item) {
	q.mu.Lock()
	q.items = append(q.items, it)
	if len(q.items) > q.peakN {
		q.peakN = len(q.items) // фиксируем пиковый backlog
	}
	q.mu.Unlock()
}

// take возвращает (элемент, есть-ли-элемент, закрыта-ли-и-пуста). Неблокирующая.
func (q *unboundedQueue) take() (item, bool, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return item{}, false, q.closed
	}
	it := q.items[0]
	q.items = q.items[1:]
	return it, true, false
}

func (q *unboundedQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}

func (q *unboundedQueue) peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peakN
}
