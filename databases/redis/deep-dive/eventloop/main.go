// Стенд #2: single-threaded event loop и производительность.
//
// Три сценария (-scenario):
//   - no-pipeline:  N команд SET по одной за раз, одним клиентом — p50/p95/p99
//     латентности отдельной команды + throughput.
//   - pipeline:     те же N команд SET через rdb.Pipeline() батчами по -batch,
//     тот же замер (латентность на слот — среднее по батчу) — сравнение с
//     no-pipeline на одном и том же сервере.
//   - threaded-io:  no-pipeline и pipeline ещё раз, но с -concurrency
//     параллельными клиентами (общий пул соединений), под конкурентной
//     нагрузкой. Сам процесс не перезапускает контейнер — io-threads задаётся
//     на старте redis-server снаружи (см. README/FIXTURES); сценарий печатает
//     фактические CONFIG GET io-threads / io-threads-do-reads сервера, к
//     которому подключился, чтобы лог был самодостаточным: какой прогон к
//     какой конфигурации относится, видно прямо в выводе.
//
// Адрес Redis/Valkey читается из REDIS_ADDR, по умолчанию 127.0.0.1:6379.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

func addrFromEnv() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	return addr
}

// must проверяет ошибку записи и завершает процесс, если она есть. Латентность,
// измеренная вокруг команды, которая на самом деле не прошла (оборванное
// соединение, таймаут), — бессмысленное число: оно измеряет что угодно, кроме
// заявленного сценария. Проще упасть сразу, чем опубликовать нечестный p99.
func must(label string, cmd redis.Cmder) {
	if err := cmd.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: ошибка записи: %v\n", label, err)
		os.Exit(1)
	}
}

// mustPipelineExec — тот же принцип для Pipeline().Exec(): проверяет и
// ошибку самого Exec, и ошибку каждой команды в батче (Exec может вернуть nil
// на верхнем уровне, но отдельная команда внутри батча — упасть).
func mustPipelineExec(label string, cmds []redis.Cmder, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: ошибка записи (Exec): %v\n", label, err)
		os.Exit(1)
	}
	for _, c := range cmds {
		if cerr := c.Err(); cerr != nil {
			fmt.Fprintf(os.Stderr, "%s: ошибка записи (cmd %s): %v\n", label, c, cerr)
			os.Exit(1)
		}
	}
}

// Stats — методология явно: p50/p95/p99 по массиву измерений одной операции
// (no-pipeline) или одного батча в пересчёте на операцию (pipeline; см.
// benchmarkWorker), throughput — n операций / общее время стенки (wall-clock)
// на всех участвующих клиентах.
type Stats struct {
	N                int
	Elapsed          time.Duration
	P50, P95, P99    time.Duration
	ThroughputOpsSec float64
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func computeStats(lat []time.Duration, elapsed time.Duration, n int) Stats {
	sorted := make([]time.Duration, len(lat))
	copy(sorted, lat)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return Stats{
		N:                n,
		Elapsed:          elapsed,
		P50:              percentile(sorted, 0.50),
		P95:              percentile(sorted, 0.95),
		P99:              percentile(sorted, 0.99),
		ThroughputOpsSec: float64(n) / elapsed.Seconds(),
	}
}

func (s Stats) print(label string) {
	fmt.Printf("%s: n=%d elapsed=%s p50=%s p95=%s p99=%s throughput=%.1f ops/s\n",
		label, s.N, s.Elapsed.Round(time.Microsecond), s.P50, s.P95, s.P99, s.ThroughputOpsSec)
}

// benchmarkWorker выполняет count операций SET начиная с ключа bench:<offset>,
// либо по одной (pipeline=false), либо батчами по batch через Pipeline
// (pipeline=true). Возвращает измерения латентности: для no-pipeline — одна
// запись на операцию; для pipeline — одна запись на батч, время которой уже
// поделено на число операций в батче (латентность "на слот", как задано в
// брифе), поэтому число записей в pipeline-режиме равно ceil(count/batch), а
// не count.
func benchmarkWorker(ctx context.Context, rdb *redis.Client, offset, count int, pipeline bool, batch int) []time.Duration {
	if pipeline {
		lat := make([]time.Duration, 0, (count+batch-1)/batch)
		for i := 0; i < count; i += batch {
			end := i + batch
			if end > count {
				end = count
			}
			t0 := time.Now()
			p := rdb.Pipeline()
			for j := i; j < end; j++ {
				p.Set(ctx, fmt.Sprintf("bench:%d", offset+j), "v", 0)
			}
			cmds, err := p.Exec(ctx)
			mustPipelineExec(fmt.Sprintf("pipeline offset=%d batch-start=%d", offset, i), cmds, err)
			actual := end - i
			lat = append(lat, time.Since(t0)/time.Duration(actual))
		}
		return lat
	}
	lat := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		t0 := time.Now()
		must(fmt.Sprintf("SET bench:%d", offset+i), rdb.Set(ctx, fmt.Sprintf("bench:%d", offset+i), "v", 0))
		lat = append(lat, time.Since(t0))
	}
	return lat
}

// benchmark — единственный клиент, последовательно, без конкуренции (для
// no-pipeline/pipeline сценариев).
func benchmark(ctx context.Context, rdb *redis.Client, n int, pipeline bool, batch int) Stats {
	start := time.Now()
	lat := benchmarkWorker(ctx, rdb, 0, n, pipeline, batch)
	elapsed := time.Since(start)
	return computeStats(lat, elapsed, n)
}

// benchmarkConcurrent делит n операций поровну между concurrency горутинами
// (каждая — свой диапазон ключей bench:<offset>..), запускает их одновременно
// на общем клиенте с пулом соединений >= concurrency (иначе конкурентные
// команды упёрлись бы в одно TCP-соединение и измеряли бы не то, что заявлено),
// throughput считается по общему времени стенки от старта первой горутины до
// финиша последней.
func benchmarkConcurrent(ctx context.Context, rdb *redis.Client, n, concurrency int, pipeline bool, batch int) Stats {
	if concurrency < 1 {
		concurrency = 1
	}
	perWorker := n / concurrency
	remainder := n % concurrency

	var wg sync.WaitGroup
	var mu sync.Mutex
	lat := make([]time.Duration, 0, n)

	start := time.Now()
	offset := 0
	for w := 0; w < concurrency; w++ {
		count := perWorker
		if w < remainder {
			count++
		}
		wg.Add(1)
		go func(off, cnt int) {
			defer wg.Done()
			local := benchmarkWorker(ctx, rdb, off, cnt, pipeline, batch)
			mu.Lock()
			lat = append(lat, local...)
			mu.Unlock()
		}(offset, count)
		offset += count
	}
	wg.Wait()
	elapsed := time.Since(start)
	return computeStats(lat, elapsed, n)
}

func configGetOne(ctx context.Context, rdb *redis.Client, name string) string {
	m, err := rdb.ConfigGet(ctx, name).Result()
	if err != nil {
		return fmt.Sprintf("<ошибка: %v>", err)
	}
	v, ok := m[name]
	if !ok {
		return "<нет такого параметра>"
	}
	return v
}

func threadedIO(ctx context.Context, rdb *redis.Client, n, concurrency, batch int) {
	fmt.Println("=== threaded-io ===")
	fmt.Printf("io-threads: %s\n", configGetOne(ctx, rdb, "io-threads"))
	fmt.Printf("io-threads-do-reads: %s\n", configGetOne(ctx, rdb, "io-threads-do-reads"))
	fmt.Printf("n=%d concurrency=%d batch=%d\n", n, concurrency, batch)

	s1 := benchmarkConcurrent(ctx, rdb, n, concurrency, false, batch)
	s1.print(fmt.Sprintf("threaded-io/no-pipeline(c=%d)", concurrency))

	s2 := benchmarkConcurrent(ctx, rdb, n, concurrency, true, batch)
	s2.print(fmt.Sprintf("threaded-io/pipeline(c=%d)", concurrency))
}

func main() {
	scenario := flag.String("scenario", "", "no-pipeline | pipeline | threaded-io")
	n := flag.Int("n", 10000, "число операций SET")
	concurrency := flag.Int("concurrency", 8, "число параллельных клиентов (только threaded-io)")
	batch := flag.Int("batch", 100, "размер батча для pipeline")
	flag.Parse()

	ctx := context.Background()

	poolSize := *concurrency * 2
	if poolSize < 16 {
		poolSize = 16
	}
	rdb := redis.NewClient(&redis.Options{Addr: addrFromEnv(), PoolSize: poolSize})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ping failed:", err)
		os.Exit(1)
	}

	switch *scenario {
	case "no-pipeline":
		fmt.Println("=== no-pipeline ===")
		fmt.Printf("n=%d\n", *n)
		s := benchmark(ctx, rdb, *n, false, *batch)
		s.print("no-pipeline")
	case "pipeline":
		fmt.Println("=== pipeline ===")
		fmt.Printf("n=%d batch=%d\n", *n, *batch)
		s := benchmark(ctx, rdb, *n, true, *batch)
		s.print("pipeline")
	case "threaded-io":
		threadedIO(ctx, rdb, *n, *concurrency, *batch)
	default:
		fmt.Fprintln(os.Stderr, "unknown -scenario, expected: no-pipeline | pipeline | threaded-io")
		os.Exit(1)
	}
}
