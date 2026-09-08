// Демо к статье «Backpressure и load shedding» (khorost.tech).
//
// Сравнение адаптивного лимитера параллелизма (AIMD) со статическим при
// зависимости, пропускная способность которой меняется во времени. Зависимость
// моделируется так: пока число одновременных вызовов не превышает её текущую
// «ёмкость», латентность низкая; при превышении — растёт пропорционально
// перегрузу (эффект очереди на стороне зависимости). Ёмкость проходит фазы
// быстро → медленно → снова быстро.
//
//   - static — фиксированный лимит: хорош, пока ёмкость зависимости высока, но в
//     «медленной» фазе продолжает слать столько же вызовов и раскачивает
//     латентность.
//   - adaptive — по замерам латентности снижает лимит в «медленной» фазе (меньше
//     перегруза зависимости, ниже латентность) и восстанавливает его, когда
//     зависимость снова быстрая.
//
// Все метрики считаются в рантайме: выполнено запросов, пропускная способность,
// средняя и p99 латентности, доля «перегруженных» вызовов, финальный лимит.
// Числа зависят от машины — снимайте своим прогоном.
//
// Запуск:
//
//	go run ./cmd/adaptive-vs-static
package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"tech.khorost/backpressure-cookbook/adaptive"
)

const (
	workers      = 32                   // сколько горутин непрерывно шлют запросы
	runDuration  = 3 * time.Second      // длительность каждого сценария
	baseLatency  = 2 * time.Millisecond // латентность зависимости в «сладкой точке»
	overloadStep = 3 * time.Millisecond // прирост латентности за каждый вызов сверх ёмкости
	threshold    = 5 * time.Millisecond // порог, выше которого считаем вызов «перегруженным»
	fastCap      = 16                   // ёмкость зависимости в быстрой фазе
	slowCap      = 4                    // ёмкость зависимости в медленной фазе
)

// dependency моделирует внешнюю зависимость с переменной ёмкостью. active — число
// вызовов, выполняемых прямо сейчас; capacity — текущая ёмкость. Возвращает
// латентность вызова.
func dependency(active, capacity int) time.Duration {
	if active <= capacity {
		return baseLatency
	}
	over := active - capacity
	return baseLatency + time.Duration(over)*overloadStep
}

// capacityAt возвращает ёмкость зависимости в момент elapsed от старта: фаза
// быстро → медленно → быстро.
func capacityAt(elapsed time.Duration) int {
	switch {
	case elapsed < runDuration/3:
		return fastCap // быстрая фаза
	case elapsed < 2*runDuration/3:
		return slowCap // медленная фаза (ёмкость зависимости просела)
	default:
		return fastCap // восстановление
	}
}

type stats struct {
	done       int64
	overloaded int64
	mu         sync.Mutex
	latencies  []time.Duration
}

func (s *stats) record(lat time.Duration) {
	atomic.AddInt64(&s.done, 1)
	if lat > threshold {
		atomic.AddInt64(&s.overloaded, 1)
	}
	s.mu.Lock()
	s.latencies = append(s.latencies, lat)
	s.mu.Unlock()
}

func main() {
	fmt.Printf("зависимость: база %v, порог перегруза %v; ёмкость по фазам %d→%d→%d за %v\n",
		baseLatency, threshold, fastCap, slowCap, fastCap, runDuration)
	fmt.Printf("рабочих горутин: %d\n\n", workers)

	// Статический лимитер провижнят под быструю фазу (Min==Max фиксируют лимит на
	// fastCap): в медленной фазе он продолжает слать столько же и перегружает.
	static := adaptive.New(adaptive.Config{Min: fastCap, Max: fastCap, Initial: fastCap, Threshold: threshold})
	// Адаптивный: тот же потолок fastCap, но в медленной фазе снижается к Min.
	adap := adaptive.New(adaptive.Config{
		Min: slowCap, Max: fastCap, Initial: fastCap, Threshold: threshold, Increase: 1, Decrease: 0.5,
	})

	staticStats, staticFinal := runScenario(static)
	adaptStats, adaptFinal := runScenario(adap)

	printReport(fmt.Sprintf("static(lim=%d)", fastCap), staticStats, staticFinal)
	printReport(fmt.Sprintf("adaptive[%d..%d]", slowCap, fastCap), adaptStats, adaptFinal)

	fmt.Println("\nВывод: в медленной фазе static держит прежний параллелизм и раскачивает")
	fmt.Println("латентность/долю перегруженных вызовов; adaptive снижает лимит и держит")
	fmt.Println("латентность ниже, восстанавливая параллелизм при норме. Числа у себя.")
}

// runScenario гоняет нагрузку через лимитер limiter в течение runDuration и
// возвращает собранную статистику и финальный лимит.
func runScenario(limiter *adaptive.Limiter) (*stats, int) {
	st := &stats{latencies: make([]time.Duration, 0, 4096)}
	var active int64 // число вызовов зависимости прямо сейчас

	ctx, cancel := context.WithTimeout(context.Background(), runDuration)
	defer cancel()
	start := time.Now()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if err := limiter.Acquire(ctx); err != nil {
					return // дедлайн сценария истёк
				}
				cur := atomic.AddInt64(&active, 1)
				lat := dependency(int(cur), capacityAt(time.Since(start)))
				busy(lat)
				atomic.AddInt64(&active, -1)
				st.record(lat)
				limiter.Release(lat)
			}
		}()
	}
	wg.Wait()
	return st, limiter.Limit()
}

// busy имитирует работу фиксированной длительности активным ожиданием.
func busy(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
	}
}

func printReport(name string, s *stats, finalLimit int) {
	done := atomic.LoadInt64(&s.done)
	over := atomic.LoadInt64(&s.overloaded)
	avg := average(s.latencies)
	p99 := percentile(s.latencies, 99)
	tput := float64(done) / runDuration.Seconds()
	overShare := 0.0
	if done > 0 {
		overShare = 100 * float64(over) / float64(done)
	}
	fmt.Printf("%-16s выполнено=%-6d %.0f req/s  avg=%-8v p99=%-8v перегружено=%.1f%%  финальный лимит=%d\n",
		name, done, tput, round(avg), round(p99), overShare, finalLimit)
}

func average(xs []time.Duration) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	var sum time.Duration
	for _, x := range xs {
		sum += x
	}
	return sum / time.Duration(len(xs))
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

func round(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }
