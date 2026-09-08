// Команда measure печатает те же числа, что проверяют тесты: стык окон
// (fixed vs sliding) и форму всплеска (token vs leaky). Запуск: go run ./cmd/measure
package main

import (
	"fmt"
	"time"

	rl "tech.khorost/ratelimit-cookbook"
)

func allowed(l rl.Limiter, now time.Time, n int) int {
	a := 0
	for i := 0; i < n; i++ {
		if l.AllowAt(now).Allowed {
			a++
		}
	}
	return a
}

func main() {
	base := time.Unix(1000, 0)
	const limit = 100
	tEnd := base.Add(900 * time.Millisecond)
	tNext := base.Add(1000 * time.Millisecond)

	fmt.Println("== Стык окон: лимит 100/сек, 100 запросов в конце окна + 100 в начале следующего ==")
	fmt.Printf("  %-18s %s\n", "алгоритм", "пропущено за стык")
	for _, c := range []struct {
		name string
		lim  rl.Limiter
	}{
		{"fixed-window", rl.NewFixedWindow(limit, time.Second)},
		{"sliding-log", rl.NewSlidingWindowLog(limit, time.Second)},
		{"sliding-counter", rl.NewSlidingWindowCounter(limit, time.Second)},
	} {
		got := allowed(c.lim, tEnd, limit) + allowed(c.lim, tNext, limit)
		note := ""
		if got > limit {
			note = fmt.Sprintf("  ← %.1f× лимита", float64(got)/limit)
		}
		fmt.Printf("  %-18s %d%s\n", c.name, got, note)
	}

	fmt.Println("\n== Форма всплеска: 10 запросов в один момент ==")
	fmt.Printf("  %-24s %d (пропущен всплеск до ёмкости)\n", "token bucket (ёмк. 5)", allowed(rl.NewTokenBucket(5, 5), base, 10))
	fmt.Printf("  %-24s %d (сглажено до темпа)\n", "leaky bucket (5/сек)", allowed(rl.NewLeakyBucket(5, 1), base, 10))
}
