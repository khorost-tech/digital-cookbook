package main

import (
	"context"
	"testing"

	"khorost.tech/graph-client-go/queries"
)

// Benchmark-обёртки для тех, кто предпочитает go test -bench / benchstat.
// Основной источник сравнительной таблицы — раннер (main.go); эти бенчи покрывают
// две показательные оси: глубину traversal и границу пути (обрыв AGE).

func connectAll(b *testing.B) map[string]queries.GraphQueries {
	b.Helper()
	cfg := queries.ConfigFromEnv()
	m := map[string]queries.GraphQueries{}
	for _, name := range []string{"neo4j", "age", "baseline"} {
		q, err := queries.New(context.Background(), name, cfg)
		if err != nil {
			b.Skipf("нет подключения к %s: %v", name, err)
		}
		m[name] = q
		b.Cleanup(func() { _ = q.Close() })
	}
	return m
}

func BenchmarkImpactByDepth(b *testing.B) {
	backends := connectAll(b)
	const target = int64(3151)
	for _, depth := range []int{2, 5, 8} {
		for _, name := range []string{"neo4j", "age", "baseline"} {
			q := backends[name]
			b.Run(name+"/depth="+itoa(depth), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := q.ImpactOf(context.Background(), target, depth); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkDistanceByBound(b *testing.B) {
	backends := connectAll(b)
	for _, bound := range []int{4, 6, 8} {
		for _, name := range []string{"neo4j", "age", "baseline"} {
			q := backends[name]
			b.Run(name+"/maxLen="+itoa(bound), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := q.Distance(context.Background(), 1, 2, bound); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
