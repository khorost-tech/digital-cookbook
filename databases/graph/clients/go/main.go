// Демо Go-клиента: гоняет пять графовых вопросов на выбранном угле (или на всех
// трёх сразу — GRAPH_BACKEND=all) и печатает результаты. Показывает главное:
// neo4j, age и baseline отвечают одинаково.
//
// Переменные окружения: GRAPH_BACKEND (neo4j|age|baseline|all), NEO4J_URI, PG_DSN.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"khorost.tech/graph-client-go/queries"
)

func main() {
	backend := os.Getenv("GRAPH_BACKEND")
	if backend == "" {
		backend = "all"
	}
	cfg := queries.ConfigFromEnv()

	backends := []string{backend}
	if backend == "all" {
		backends = []string{"neo4j", "age", "baseline"}
	}

	for _, b := range backends {
		connCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		q, err := queries.New(connCtx, b, cfg)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] подключение: %v\n", b, err)
			continue
		}
		runDemo(q)
		_ = q.Close()
	}
}

// perQuery — свой таймаут на каждый запрос, чтобы медленный угол (например,
// AGE на shortest-path без shortestPath) не срывал остальные измерения.
const perQuery = 20 * time.Second

func runDemo(q queries.GraphQueries) {
	b := q.Backend()
	fmt.Printf("\n=== backend: %s ===\n", b)

	withCtx := func(fn func(context.Context)) {
		ctx, cancel := context.WithTimeout(context.Background(), perQuery)
		defer cancel()
		fn(ctx)
	}

	withCtx(func(ctx context.Context) {
		access, err := q.AccessibleResources(ctx, 1)
		report(b, "AccessibleResources(user=1)", len(access), sample(access), err)
	})

	var cyc []int64
	withCtx(func(ctx context.Context) {
		var err error
		cyc, err = q.CyclicServices(ctx, 15)
		report(b, "CyclicServices(maxLen=15)", len(cyc), sample(cyc), err)
	})

	if len(cyc) > 0 {
		withCtx(func(ctx context.Context) {
			impact, err := q.ImpactOf(ctx, cyc[0], 10)
			report(b, fmt.Sprintf("ImpactOf(service=%d, depth=10)", cyc[0]), len(impact), sample(impact), err)
		})
	}

	withCtx(func(ctx context.Context) {
		dist, err := q.Distance(ctx, 1, 2, 6)
		if err != nil {
			fmt.Printf("[%s] Distance(1,2, maxLen=6): %v\n", b, err)
		} else {
			fmt.Printf("[%s] Distance(1,2, maxLen=6) = %d\n", b, dist)
		}
	})

	withCtx(func(ctx context.Context) {
		ring, err := q.RingMembers(ctx, 4)
		report(b, "RingMembers(length=4)", len(ring), sample(ring), err)
	})
}

func report(backend, name string, count int, sample []int64, err error) {
	if err != nil {
		fmt.Printf("[%s] %s: ошибка: %v\n", backend, name, err)
		return
	}
	fmt.Printf("[%s] %-32s → %d шт., пример %v\n", backend, name, count, sample)
}

func sample(ids []int64) []int64 {
	if len(ids) > 5 {
		return ids[:5]
	}
	return ids
}
