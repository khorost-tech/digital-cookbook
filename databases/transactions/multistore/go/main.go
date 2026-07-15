// Стенд №2 к финальной статье #5 «Мульти-хранилищный стенд и карта выбора».
// https://khorost.tech/databases/transactions-multistore-benchmark-decision-map/
//
// Один сценарий по всем хранилищам одним нагрузчиком: «списать 1 при инварианте баланс ≥ 0».
// budget попыток превышает начальный баланс, поэтому часть списаний должна быть отклонена.
// Корректный механизм каждого хранилища: succeeded == budget, final == 0, инвариант не нарушен.
// Сравниваем цену корректности: throughput и latency.
//
//	go run . -store all -workers 16 -iters 100 -budget 1000
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Store — общий интерфейс адаптера хранилища для сценария «списать если баланс > 0».
type Store interface {
	Name() string
	Reset(ctx context.Context, budget int64) error
	Decrement(ctx context.Context) (applied bool, conflicts int64) // атомарно
	Final(ctx context.Context) int64
	Close()
}

type Result struct {
	store     string
	succeeded int64 // авторитетно из БД: budget - final (физически списанное)
	final     int64
	conflicts int64
	p50, p99  time.Duration
	tps       float64
}

func bench(ctx context.Context, s Store, workers, iters int, budget int64) Result {
	if err := s.Reset(ctx, budget); err != nil {
		fmt.Printf("%s reset: %v\n", s.Name(), err)
	}
	var conflicts int64
	var mu sync.Mutex
	var lat []time.Duration
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				t0 := time.Now()
				_, c := s.Decrement(ctx)
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				mu.Unlock()
				atomic.AddInt64(&conflicts, c)
			}
		}()
	}
	wg.Wait()
	wall := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(float64(len(lat)-1)*p)]
	}
	final := s.Final(ctx)
	// succeeded считаем по факту в БД (budget - final), а не по клиентскому счётчику: под жёсткой
	// LWT-контензией клиентские applied ненадёжны, а баланс в БД — авторитетная истина.
	return Result{
		store: s.Name(), succeeded: budget - final, final: final, conflicts: conflicts,
		p50: pct(0.50), p99: pct(0.99), tps: float64(workers*iters) / wall.Seconds(),
	}
}

func main() {
	store := flag.String("store", "all", "pg|redis|mongo|scylla|all")
	workers := flag.Int("workers", 16, "воркеров")
	iters := flag.Int("iters", 100, "попыток на воркер")
	budget := flag.Int64("budget", 1000, "начальный баланс (< workers*iters)")
	flag.Parse()

	ctx := context.Background()
	var stores []Store
	add := func(name string, ctor func() (Store, error)) {
		if *store == "all" || *store == name {
			s, err := ctor()
			if err != nil {
				fmt.Printf("%s: %v\n", name, err)
				return
			}
			stores = append(stores, s)
		}
	}
	add("pg", newPG)
	add("redis", newRedis)
	add("mongo", newMongo)
	add("scylla", newScylla)
	if len(stores) == 0 {
		fmt.Println("нет доступных хранилищ")
		os.Exit(1)
	}

	attempts := (*workers) * (*iters)
	fmt.Printf("сценарий: budget=%d, %d воркеров × %d = %d попыток «списать если баланс>0»\n\n",
		*budget, *workers, *iters, attempts)
	fmt.Printf("%-8s %10s %6s %11s %9s %9s  %s\n", "store", "succeeded", "final", "throughput", "p50", "p99", "инвариант")
	for _, s := range stores {
		r := bench(ctx, s, *workers, *iters, *budget)
		// инвариант: баланс слит ровно до нуля и никогда не уходил в минус (final == 0)
		inv := "✓"
		if r.final != 0 {
			inv = "✗ НАРУШЕН"
		}
		fmt.Printf("%-8s %10d %6d %8.0f/s %9v %9v  %s (conflicts=%d)\n",
			r.store, r.succeeded, r.final, r.tps, r.p50, r.p99, inv, r.conflicts)
		s.Close()
	}
}
