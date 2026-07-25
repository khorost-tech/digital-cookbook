// Command mergetree — стенд #2 серии "ClickHouse: глубокое погружение":
// MergeTree из Go — вставки и запросы. DDL с явными ORDER BY/PARTITION
// BY/index_granularity, разреженный первичный индекс (гранулы, EXPLAIN
// indexes=1 + system.query_log read_rows), батч vs построчная вставка
// (анти-паттерн), async_insert (серверная буферизация + видимость).
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../../ops/mergetree-demo.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/go/mergetree:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=all -csv=/data/events-mergetree.csv -expect-rows=5000000 \
//	  -ch-addr=clickhouse:9000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func main() {
	phase := flag.String("phase", "all", "schema|load|granules|antipattern|async|all")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr")
	csvPath := flag.String("csv", "/data/events-mergetree.csv", "путь к CSV-датасету (dataset/main.go)")
	bulkBatch := flag.Int("bulk-batch", 100_000, "размер батча bulk-загрузки (Step 2 брифа: 100k)")
	expectRows := flag.Int64("expect-rows", 0, "если >0, ассерт: число загруженных строк == это значение")
	smallN := flag.Int("small-n", 2000, "число строк для apples-to-apples сравнения батч vs построчная вставка")
	asyncTotal := flag.Int("async-total", 2000, "число async-вставок в конкурентном сценарии буферизации")
	asyncConcurrency := flag.Int("async-concurrency", 50, "конкурентность async-вставок (имитация многих независимых писателей)")
	asyncBusyMs := flag.Int("async-busy-ms", 200, "async_insert_busy_timeout_ms для конкурентного сценария")
	visTotal := flag.Int("vis-total", 500, "число async-вставок в сценарии видимости (wait_for_async_insert=0)")
	visBusyMs := flag.Int("vis-busy-ms", 5000, "async_insert_busy_timeout_ms для сценария видимости")
	flag.Parse()

	ctx := context.Background()
	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{*chAddr},
		Auth:        clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout: 10 * time.Second,
		Settings:    clickhouse.Settings{"max_execution_time": 120},
	})
	if err != nil {
		log.Fatalf("ch open: %v", err)
	}
	defer ch.Close()
	if err := ch.Ping(ctx); err != nil {
		log.Fatalf("ch ping: %v", err)
	}

	switch *phase {
	case "schema":
		mustRun(func() error { return createTable(ctx, ch, "mergetree_events") })
	case "load":
		phaseLoad(ctx, ch, *csvPath, *bulkBatch, *expectRows)
	case "granules":
		phaseGranules(ctx, ch, *expectRows)
	case "antipattern":
		phaseAntiPattern(ctx, ch, *csvPath, *smallN)
	case "async":
		phaseAsync(ctx, ch, *asyncTotal, *asyncConcurrency, *asyncBusyMs, *visTotal, *visBusyMs)
	case "all":
		mustRun(func() error { return createTable(ctx, ch, "mergetree_events") })
		phaseLoad(ctx, ch, *csvPath, *bulkBatch, *expectRows)
		phaseGranules(ctx, ch, *expectRows)
		phaseAntiPattern(ctx, ch, *csvPath, *smallN)
		phaseAsync(ctx, ch, *asyncTotal, *asyncConcurrency, *asyncBusyMs, *visTotal, *visBusyMs)
		fmt.Println("\n[mergetree] all phases completed, all asserts passed")
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase=%s\n", *phase)
		os.Exit(2)
	}
}

func mustRun(f func() error) {
	if err := f(); err != nil {
		log.Fatalf("%v", err)
	}
}

func phaseLoad(ctx context.Context, ch clickhouse.Conn, csvPath string, batchSize int, expectRows int64) {
	fmt.Println("\n=== LOAD (batch, Step 2 брифа) ===")
	rows, dur, err := batchLoad(ctx, ch, "mergetree_events", csvPath, batchSize, 0)
	if err != nil {
		log.Fatalf("bulk load: %v", err)
	}
	parts, err := activeParts(ctx, ch, "mergetree_events")
	if err != nil {
		log.Fatalf("active parts: %v", err)
	}
	fmt.Printf("[load] mergetree_events: %d rows in %s (%.0f rows/s), batch=%d, %d active parts\n",
		rows, dur, float64(rows)/dur.Seconds(), batchSize, parts)

	if expectRows > 0 {
		assertFailFast(int64(rows) == expectRows, "loaded rows (%d) == expected (-expect-rows=%d)", rows, expectRows)
	}
}

func phaseGranules(ctx context.Context, ch clickhouse.Conn, expectRows int64) {
	fmt.Println("\n=== ГРАНУЛЫ: фильтр по префиксу ORDER BY (event_time) — Step 1 брифа ===")

	totalRows, err := tableRows(ctx, ch, "mergetree_events")
	if err != nil {
		log.Fatalf("table rows: %v", err)
	}
	fmt.Printf("[granules] demo.mergetree_events: %d rows всего (system.parts)\n", totalRows)

	explainFiltered, err := explainIndexes(ctx, ch, granuleFilterSQL)
	if err != nil {
		log.Fatalf("explain filtered: %v", err)
	}
	fmt.Println("[granules] EXPLAIN indexes=1 (WHERE event_time в окне 1 суток):")
	fmt.Println(explainFiltered)

	explainFull, err := explainIndexes(ctx, ch, granuleFullSQL)
	if err != nil {
		log.Fatalf("explain full: %v", err)
	}
	fmt.Println("[granules] EXPLAIN indexes=1 (без фильтра, полный скан):")
	fmt.Println(explainFull)

	filtered, err := runWithReadRows(ctx, ch, granuleFilterSQL, "mergetree-granules-filtered")
	if err != nil {
		log.Fatalf("run filtered: %v", err)
	}
	fmt.Printf("[granules] filtered query: %s, count()=%d, read_rows=%d (из %d total, %.2f%%)\n",
		filtered.duration, filtered.count, filtered.readRows, totalRows, 100*float64(filtered.readRows)/float64(totalRows))

	full, err := runWithReadRows(ctx, ch, granuleFullSQL, "mergetree-granules-full")
	if err != nil {
		log.Fatalf("run full: %v", err)
	}
	fmt.Printf("[granules] full-scan query: %s, count()=%d, read_rows=%d (из %d total, %.2f%%)\n",
		full.duration, full.count, full.readRows, totalRows, 100*float64(full.readRows)/float64(totalRows))

	assertFailFast(filtered.readRows > 0, "filtered query read_rows > 0 (query_log populated)")
	assertFailFast(filtered.readRows < totalRows/10, "granule skip: filtered read_rows (%d) < total/10 (%d из %d)", filtered.readRows, totalRows/10, totalRows)
	assertFailFast(full.readRows >= totalRows, "full-scan query reads >= весь datasest: read_rows (%d) >= total (%d)", full.readRows, totalRows)
	if expectRows > 0 {
		assertFailFast(int64(totalRows) == expectRows, "table rows (%d) == expected (-expect-rows=%d)", totalRows, expectRows)
	}
}

func phaseAntiPattern(ctx context.Context, ch clickhouse.Conn, csvPath string, n int) {
	fmt.Println("\n=== БАТЧ vs ПОСТРОЧНАЯ ВСТАВКА (анти-паттерн, Step 2 брифа) ===")

	const rowByRowTable = "mergetree_events_rowbyrow"
	const batchSmallTable = "mergetree_events_batch_small"

	mustRun(func() error { return createTable(ctx, ch, rowByRowTable) })
	// SYSTEM STOP MERGES — иначе фоновые merge'и могут схлопнуть parts ещё
	// до того, как мы их посчитаем, и "много parts" перестанет быть видно
	// (существующая проблема — свойство MergeTree, а не искусственная пауза
	// для теста: просто фиксируем состояние ДО того, как фон его сгладит).
	if err := ch.Exec(ctx, fmt.Sprintf("SYSTEM STOP MERGES demo.%s", rowByRowTable)); err != nil {
		log.Fatalf("stop merges: %v", err)
	}
	rowByRowRows, rowByRowDur, err := rowByRowLoad(ctx, ch, rowByRowTable, csvPath, int64(n))
	if err != nil {
		log.Fatalf("row-by-row load: %v", err)
	}
	rowByRowParts, err := activeParts(ctx, ch, rowByRowTable)
	if err != nil {
		log.Fatalf("row-by-row active parts: %v", err)
	}
	if err := ch.Exec(ctx, fmt.Sprintf("SYSTEM START MERGES demo.%s", rowByRowTable)); err != nil {
		log.Fatalf("start merges: %v", err)
	}
	fmt.Printf("[antipattern] row-by-row: %d rows in %s (%.1f rows/s), %d active parts (SYSTEM STOP MERGES во время вставки, чтобы зафиксировать состояние до фонового merge)\n",
		rowByRowRows, rowByRowDur, float64(rowByRowRows)/rowByRowDur.Seconds(), rowByRowParts)

	mustRun(func() error { return createTable(ctx, ch, batchSmallTable) })
	batchRows, batchDur, err := batchLoad(ctx, ch, batchSmallTable, csvPath, n, int64(n))
	if err != nil {
		log.Fatalf("batch-small load: %v", err)
	}
	batchParts, err := activeParts(ctx, ch, batchSmallTable)
	if err != nil {
		log.Fatalf("batch-small active parts: %v", err)
	}
	fmt.Printf("[antipattern] batch (1 PrepareBatch/Send, N=%d): %d rows in %s (%.1f rows/s), %d active parts (1 part на затронутую партицию toYYYYMM(event_time), не 1 part на вставку)\n",
		n, batchRows, batchDur, float64(batchRows)/batchDur.Seconds(), batchParts)

	ratio := float64(rowByRowDur) / float64(batchDur)
	fmt.Printf("[antipattern] batch faster than row-by-row (same N=%d): %.1fx (%s vs %s)\n", n, ratio, batchDur, rowByRowDur)

	assertFailFast(int64(rowByRowRows) == int64(n), "row-by-row loaded rows (%d) == N (%d)", rowByRowRows, n)
	assertFailFast(int64(batchRows) == int64(n), "batch-small loaded rows (%d) == N (%d)", batchRows, n)
	assertFailFast(ratio >= 3.0, "batch insert multiple times faster than row-by-row for same N: ratio %.1fx >= 3.0x (batch %s vs row-by-row %s)", ratio, batchDur, rowByRowDur)
	// batchParts обычно НЕ 1: одна PrepareBatch/Send-вставка создаёт один
	// part НА КАЖДУЮ затронутую партицию (toYYYYMM(event_time)) — N=2000
	// строк из 90-дневного окна датасета обычно задевает 2-4 месяца/партиции.
	// Порог <=6 — это "число партиций", а не "1 вставка = 1 part".
	assertFailFast(batchParts <= 6, "batch insert (single PrepareBatch/Send) produces few parts — one per touched partition, not one per row: %d <= 6", batchParts)
	assertFailFast(rowByRowParts > batchParts*10, "row-by-row insert produces many more parts than batch for same N: %d > %d*10", rowByRowParts, batchParts)
}

func phaseAsync(ctx context.Context, ch clickhouse.Conn, asyncTotal, asyncConcurrency, asyncBusyMs, visTotal, visBusyMs int) {
	fmt.Println("\n=== ASYNC INSERT: серверная буферизация (Step 3 брифа) ===")

	const asyncTable = "mergetree_events_async"
	mustRun(func() error { return createTable(ctx, ch, asyncTable) })
	asyncDur, err := concurrentAsyncInsert(ctx, ch, asyncTable, asyncTotal, asyncConcurrency, true, asyncBusyMs)
	if err != nil {
		log.Fatalf("concurrent async insert: %v", err)
	}
	// wait_for_async_insert=1 гарантирует, что к моменту завершения всех
	// вызовов данные уже сброшены на диск — можно сразу читать rows/parts.
	asyncRows, err := tableRows(ctx, ch, asyncTable)
	if err != nil {
		log.Fatalf("async table rows: %v", err)
	}
	asyncParts, err := activeParts(ctx, ch, asyncTable)
	if err != nil {
		log.Fatalf("async table parts: %v", err)
	}
	fmt.Printf("[async] concurrent async_insert=1/wait_for_async_insert=1: %d однострочных INSERT (concurrency=%d, async_insert_busy_timeout_ms=%d) за %s, %d rows, %d active parts (сервер скоалесил конкурентные вставки в разы меньше parts, чем %d)\n",
		asyncTotal, asyncConcurrency, asyncBusyMs, asyncDur, asyncRows, asyncParts, asyncTotal)

	assertFailFast(int64(asyncRows) == int64(asyncTotal), "async table rows (%d) == total inserted (%d) — wait_for_async_insert=1 гарантирует видимость", asyncRows, asyncTotal)
	assertFailFast(int64(asyncParts) < int64(asyncTotal)/2, "server-side buffering: active parts (%d) << number of individual inserts (%d)", asyncParts, asyncTotal)

	fmt.Println("\n=== ASYNC INSERT: видимость (wait_for_async_insert=0) ===")
	const visTable = "mergetree_events_async_vis"
	mustRun(func() error { return createTable(ctx, ch, visTable) })
	visDur, err := sequentialAsyncInsert(ctx, ch, visTable, visTotal, false, visBusyMs)
	if err != nil {
		log.Fatalf("sequential async insert (wait=0): %v", err)
	}
	immediateRows, err := tableRows(ctx, ch, visTable)
	if err != nil {
		log.Fatalf("vis table rows (immediate): %v", err)
	}
	fmt.Printf("[async-vis] отправлено %d INSERT (wait_for_async_insert=0, async_insert_busy_timeout_ms=%d) за %s (клиент не ждёт флаша); сразу после — видно %d/%d rows (частичная видимость: буфер ещё не сброшен)\n",
		visTotal, visBusyMs, visDur, immediateRows, visTotal)

	sleepFor := time.Duration(visBusyMs)*time.Millisecond + 1500*time.Millisecond
	time.Sleep(sleepFor)
	afterRows, err := tableRows(ctx, ch, visTable)
	if err != nil {
		log.Fatalf("vis table rows (after sleep): %v", err)
	}
	fmt.Printf("[async-vis] после ожидания %s (>= async_insert_busy_timeout_ms=%dms) — видно %d/%d rows (буфер сброшен, eventual consistency)\n",
		sleepFor, visBusyMs, afterRows, visTotal)

	assertFailFast(int64(immediateRows) < int64(visTotal), "immediately after wait_for_async_insert=0 inserts, visible rows (%d) < total sent (%d) — partial visibility", immediateRows, visTotal)
	assertFailFast(int64(afterRows) == int64(visTotal), "after waiting past async_insert_busy_timeout_ms, visible rows (%d) == total sent (%d) — eventual consistency", afterRows, visTotal)
}
