// Bench-харнес — источник чисел-эталона для статьи. Гоняет одни и те же запросы
// на трёх углах (neo4j / age / baseline) по честным осям: глубина traversal,
// длина цикла, длина кольца, граница пути. Числа сравнимы, потому что harness один.
//
// Замер честный: прогрев вне таймера, несколько повторов, берётся медиана. На
// сценариях, где угол взрывается (AGE без shortestPath), число повторов снижается
// автоматически, а статус помечается timeout — это данные, а не сбой.
//
// Переменные окружения: NEO4J_URI, NEO4J_USER, NEO4J_PASS, PG_DSN,
// BENCH_REPEATS (по умолчанию 7), BENCH_TIMEOUT_MS (по умолчанию 30000),
// BENCH_OUT (по умолчанию ./out/results.csv).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"khorost.tech/graph-client-go/queries"
)

type op func(ctx context.Context, q queries.GraphQueries) (int, error)

type scenario struct {
	name string
	op   op
}

type result struct {
	scenario string
	backend  string
	medianMs float64
	minMs    float64
	count    int
	status   string // ok | timeout | error
}

func main() {
	cfg := queries.ConfigFromEnv()
	repeats := envInt("BENCH_REPEATS", 7)
	timeout := time.Duration(envInt("BENCH_TIMEOUT_MS", 30000)) * time.Millisecond
	outPath := env("BENCH_OUT", "./out/results.csv")

	ctx := context.Background()

	// подключаем три угла
	backends := map[string]queries.GraphQueries{}
	for _, b := range []string{"neo4j", "age", "baseline"} {
		q, err := queries.New(ctx, b, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "подключение %s: %v\n", b, err)
			os.Exit(1)
		}
		backends[b] = q
		defer q.Close()
	}

	// целевой сервис на цикле зависимостей — для impact/depth
	target := int64(3151)
	if cyc, err := backends["baseline"].CyclicServices(ctx, 15); err == nil && len(cyc) > 0 {
		target = cyc[0]
	}

	scenarios := []scenario{
		{"access", func(ctx context.Context, q queries.GraphQueries) (int, error) {
			r, e := q.AccessibleResources(ctx, 1)
			return len(r), e
		}},
	}
	// impact по глубине traversal
	for _, d := range []int{1, 2, 3, 5, 8} {
		d := d
		scenarios = append(scenarios, scenario{
			fmt.Sprintf("impact_depth=%d", d),
			func(ctx context.Context, q queries.GraphQueries) (int, error) {
				r, e := q.ImpactOf(ctx, target, d)
				return len(r), e
			}})
	}
	// цикл по границе длины
	for _, m := range []int{5, 10, 15} {
		m := m
		scenarios = append(scenarios, scenario{
			fmt.Sprintf("cyclic_maxLen=%d", m),
			func(ctx context.Context, q queries.GraphQueries) (int, error) {
				r, e := q.CyclicServices(ctx, m)
				return len(r), e
			}})
	}
	// расстояние по границе пути — здесь виден обрыв AGE (нет shortestPath)
	for _, m := range []int{4, 6, 8} {
		m := m
		scenarios = append(scenarios, scenario{
			fmt.Sprintf("distance_maxLen=%d", m),
			func(ctx context.Context, q queries.GraphQueries) (int, error) {
				_, e := q.Distance(ctx, 1, 2, m)
				return 0, e
			}})
	}
	// кольца по длине — fraud, дорогой обход с fan-out
	for _, l := range []int{3, 4} {
		l := l
		scenarios = append(scenarios, scenario{
			fmt.Sprintf("ring_length=%d", l),
			func(ctx context.Context, q queries.GraphQueries) (int, error) {
				r, e := q.RingMembers(ctx, l)
				return len(r), e
			}})
	}

	var results []result
	order := []string{"neo4j", "age", "baseline"}
	for _, sc := range scenarios {
		for _, b := range order {
			results = append(results, measure(sc, b, backends[b], repeats, timeout))
		}
	}

	printTable(results, order)
	writeCSV(outPath, results)
}

// measure прогревает и замеряет один (сценарий, backend). Если прогрев близок к
// таймауту, число повторов снижается — чтобы взрыв на одном угле не растянул прогон.
func measure(sc scenario, backend string, q queries.GraphQueries, repeats int, timeout time.Duration) result {
	res := result{scenario: sc.name, backend: backend, status: "ok"}

	warmDur, cnt, err := runOnce(sc.op, q, timeout)
	if err != nil {
		if err == context.DeadlineExceeded {
			res.status = "timeout"
			res.medianMs = float64(timeout.Milliseconds())
			res.minMs = res.medianMs
			return res
		}
		res.status = "error"
		return res
	}
	res.count = cnt

	n := repeats
	if warmDur > 3*time.Second { // дорогой сценарий — не гоняем много раз
		n = 2
	}

	durs := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		d, _, e := runOnce(sc.op, q, timeout)
		if e != nil {
			if e == context.DeadlineExceeded {
				res.status = "timeout"
				res.medianMs = float64(timeout.Milliseconds())
				res.minMs = res.medianMs
				return res
			}
			res.status = "error"
			return res
		}
		durs = append(durs, d)
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	res.medianMs = ms(durs[len(durs)/2])
	res.minMs = ms(durs[0])
	return res
}

func runOnce(o op, q queries.GraphQueries, timeout time.Duration) (time.Duration, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	cnt, err := o(ctx, q)
	return time.Since(start), cnt, err
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

func printTable(results []result, order []string) {
	fmt.Printf("\n%-22s %-10s %12s %12s %8s %-8s\n", "scenario", "backend", "median_ms", "min_ms", "count", "status")
	fmt.Println("---------------------------------------------------------------------------------")
	for i, r := range results {
		fmt.Printf("%-22s %-10s %12.2f %12.2f %8d %-8s\n",
			r.scenario, r.backend, r.medianMs, r.minMs, r.count, r.status)
		if (i+1)%len(order) == 0 {
			fmt.Println()
		}
	}
}

func writeCSV(path string, results []result) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir out:", err)
		return
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create csv:", err)
		return
	}
	defer f.Close()
	fmt.Fprintln(f, "scenario,backend,median_ms,min_ms,count,status")
	for _, r := range results {
		fmt.Fprintf(f, "%s,%s,%.2f,%.2f,%d,%s\n",
			r.scenario, r.backend, r.medianMs, r.minMs, r.count, r.status)
	}
	fmt.Printf("\nCSV: %s\n", path)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
