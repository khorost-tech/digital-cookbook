// Демо к статье «Resilience-паттерны»: каскадный отказ и circuit breaker.
//
// Зависимость «висит» — каждый вызов блокируется на depLatency и падает (худший
// случай: не быстрый отказ, а медленный таймаут). Сервис принимает запросы в
// пул ограниченного размера. Сравниваются два режима на одинаковой нагрузке:
//
//   - без breaker: каждый запрос занимает воркера и висит на мёртвой зависимости
//     depLatency. Воркеры кончаются, пул исчерпан, входящие запросы отклоняются —
//     goodput падает почти до нуля, а p99 упирается в depLatency.
//   - с breaker: после серии отказов цепь размыкается, запросы мгновенно уходят
//     в fallback, не трогая зависимость и не держа воркеров, — goodput и p99 держатся.
//
// Все числа считаются в рантайме; абсолютные значения зависят от машины — важны
// соотношения (goodput с breaker в разы выше, p99 в разы ниже). Запуск:
//
//	go run ./cmd/cascade
package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"tech.khorost/patterns-cookbook/breaker"
)

const (
	poolSize   = 20                     // размер пула воркеров сервиса
	depLatency = 200 * time.Millisecond // «висящая» зависимость: столько ждём до отказа
	offered    = 2000                   // сколько запросов подать
	interval   = 250 * time.Microsecond // интервал подачи (open-loop) ≈ 4000 rps
)

type report struct {
	name      string
	served    int64         // отдали ответ (здесь это всегда fallback: зависимость лежит)
	rejected  int64         // не нашлось воркера — отклонили сразу
	offeredIn time.Duration // фактическое время подачи нагрузки
	latencies []time.Duration
	wall      time.Duration
}

func main() {
	fmt.Printf("нагрузка: %d запросов через %v (≈%.0f rps), пул %d, зависимость висит %v\n\n",
		offered, interval, float64(time.Second)/float64(interval), poolSize, depLatency)
	printReport(run("без breaker", false))
	printReport(run("с breaker", true))
	fmt.Println("Вывод: breaker меняет медленный отказ на быстрый fallback — воркеры не")
	fmt.Println("исчерпываются, поэтому сервис продолжает отвечать, а p50 не упирается в")
	fmt.Println("латентность мёртвой зависимости. ВАЖНО: это ответы-заглушки, полезность")
	fmt.Println("fallback здесь не моделируется. И числа, и КРАТНОСТЬ зависят от машины —")
	fmt.Println("снимайте своим прогоном, разы у вас будут другие.")
}

// run подаёт нагрузку в open-loop и собирает метрики одного режима.
func run(name string, withBreaker bool) report {
	sem := make(chan struct{}, poolSize)
	var br *breaker.Breaker
	if withBreaker {
		// разомкнуться после 10 отказов, пауза 200мс, 2 пробы.
		br = breaker.New(10, 200*time.Millisecond, 2)
	}

	var served, rejected int64
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, offered)
	var wg sync.WaitGroup

	start := time.Now()
	genStart := time.Now()
	for i := 0; i < offered; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t0 := time.Now()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			default:
				atomic.AddInt64(&rejected, 1) // пул исчерпан — быстрый отказ приёма
				return
			}
			// Есть воркер. Если breaker открыт — мгновенный fallback.
			if br != nil && !br.AllowAt(time.Now()) {
				record(&mu, &latencies, time.Since(t0))
				atomic.AddInt64(&served, 1)
				return
			}
			// Иначе идём в зависимость: она висит depLatency и падает.
			time.Sleep(depLatency)
			if br != nil {
				br.Record(time.Now(), false)
			}
			// После отказа отдаём fallback — но уже потратив depLatency и воркера.
			record(&mu, &latencies, time.Since(t0))
			atomic.AddInt64(&served, 1)
		}()
		time.Sleep(interval)
	}
	genElapsed := time.Since(genStart)
	wg.Wait()
	return report{name, served, rejected, genElapsed, latencies, time.Since(start)}
}

func record(mu *sync.Mutex, s *[]time.Duration, d time.Duration) {
	mu.Lock()
	*s = append(*s, d)
	mu.Unlock()
}

func printReport(r report) {
	sort.Slice(r.latencies, func(i, j int) bool { return r.latencies[i] < r.latencies[j] })
	goodput := float64(r.served) / r.wall.Seconds()
	actualRate := float64(offered) / r.offeredIn.Seconds()
	fmt.Printf("== %s ==\n", r.name)
	fmt.Printf("  фактический входной темп: %.0f rps (номинально %.0f — Sleep не точен)\n",
		actualRate, float64(time.Second)/float64(interval))
	fmt.Printf("  отдано ответов: %d  отклонено (нет воркера): %d\n", r.served, r.rejected)
	fmt.Printf("  ответов/с: %.0f  ← это ответы-заглушки (fallback), а не успешная работа\n", goodput)
	fmt.Printf("  латентность p50: %v  p99: %v\n\n", pct(r.latencies, 50), pct(r.latencies, 99))
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (p * len(sorted)) / 100
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i].Round(time.Millisecond)
}
