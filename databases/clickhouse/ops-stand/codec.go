package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// phaseCodec — Step 4 брифа: codec-сжатие CODEC(ZSTD(3)) vs CODEC(Delta,
// ZSTD(3)) для event_time — реальный размер колонки до/после (system.columns
// data_compressed_bytes). Плюс бонус того же Step 4: index_granularity и
// max_threads/лимит памяти запроса (phaseGranularity/phaseThreadsMemory).
func phaseCodec(ctx context.Context, ch clickhouse.Conn, csvPath string, skip int, limit int64) {
	fmt.Println("\n=== CODEC: ZSTD(3) vs Delta+ZSTD(3) для event_time (Step 4 брифа) ===")

	const zstdTable = "ops_codec_zstd"
	const deltaTable = "ops_codec_delta"

	mustRun(func() error { return createTable(ctx, ch, zstdTable, "ZSTD(3)", 8192) })
	mustRun(func() error { return createTable(ctx, ch, deltaTable, "Delta, ZSTD(3)", 8192) })

	zstdRows, zstdDur, err := batchLoad(ctx, ch, zstdTable, csvPath, skip, 100_000, limit)
	if err != nil {
		log.Fatalf("load codec zstd: %v", err)
	}
	deltaRows, deltaDur, err := batchLoad(ctx, ch, deltaTable, csvPath, skip, 100_000, limit)
	if err != nil {
		log.Fatalf("load codec delta: %v", err)
	}
	fmt.Printf("[codec] ZSTD(3): %d rows in %s; Delta,ZSTD(3): %d rows in %s (одинаковый диапазон CSV: skip=%d limit=%d)\n",
		zstdRows, zstdDur, deltaRows, deltaDur, skip, limit)

	mustRun(func() error { return ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", zstdTable)) })
	mustRun(func() error { return ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", deltaTable)) })

	zstdCompressed, zstdUncompressed, err := columnBytes(ctx, ch, zstdTable, "event_time")
	if err != nil {
		log.Fatalf("column bytes zstd: %v", err)
	}
	deltaCompressed, deltaUncompressed, err := columnBytes(ctx, ch, deltaTable, "event_time")
	if err != nil {
		log.Fatalf("column bytes delta: %v", err)
	}

	fmt.Printf("[codec] event_time CODEC(ZSTD(3)):        compressed=%s uncompressed=%s (ratio %.2fx)\n",
		humanBytes(zstdCompressed), humanBytes(zstdUncompressed), float64(zstdUncompressed)/float64(zstdCompressed))
	fmt.Printf("[codec] event_time CODEC(Delta, ZSTD(3)): compressed=%s uncompressed=%s (ratio %.2fx)\n",
		humanBytes(deltaCompressed), humanBytes(deltaUncompressed), float64(deltaUncompressed)/float64(deltaCompressed))

	reduction := 100 * (1 - float64(deltaCompressed)/float64(zstdCompressed))
	fmt.Printf("[codec] Delta+ZSTD компактнее ZSTD-only на event_time на %.1f%% (compressed %s vs %s) — временной ряд почти монотонен внутри part (ORDER BY (event_time,...)), Delta кодирует разницы соседних значений, ZSTD добивает остаток\n",
		reduction, humanBytes(deltaCompressed), humanBytes(zstdCompressed))

	assertFailFast(int64(zstdRows) == int64(deltaRows), "одинаковое число строк в обеих codec-таблицах: %d == %d", zstdRows, deltaRows)
	assertFailFast(deltaCompressed < zstdCompressed, "Delta+ZSTD компактнее ZSTD-only для временного ряда event_time: %s < %s", humanBytes(deltaCompressed), humanBytes(zstdCompressed))

	phaseGranularity(ctx, ch, csvPath)
	phaseThreadsMemory(ctx, ch, zstdTable)
}

// phaseGranularity — бонус Step 4 брифа: index_granularity влияет на
// разреженность первичного индекса — меньшая гранула читает не больше строк
// на узком фильтре, чем большая (та же схема запроса, что ../go/mergetree
// granules.go, тут — сравнение ДВУХ таблиц с разным granularity на одном и
// том же датасете).
func phaseGranularity(ctx context.Context, ch clickhouse.Conn, csvPath string) {
	fmt.Println("\n=== index_granularity: fine (128) vs coarse (8192) — бонус Step 4 брифа ===")

	const fineTable = "ops_gran_fine"
	const coarseTable = "ops_gran_coarse"
	const rows = 200_000

	mustRun(func() error { return createTable(ctx, ch, fineTable, "", 128) })
	mustRun(func() error { return createTable(ctx, ch, coarseTable, "", 8192) })

	fineRows, _, err := batchLoad(ctx, ch, fineTable, csvPath, 0, 50_000, rows)
	if err != nil {
		log.Fatalf("load gran fine: %v", err)
	}
	coarseRows, _, err := batchLoad(ctx, ch, coarseTable, csvPath, 0, 50_000, rows)
	if err != nil {
		log.Fatalf("load gran coarse: %v", err)
	}

	filterSQL := func(table string) string {
		return fmt.Sprintf(`SELECT count() FROM demo.%s WHERE event_time >= '2026-06-15 00:00:00' AND event_time < '2026-06-16 00:00:00'`, table)
	}

	fineStat, err := runCountWithReadRows(ctx, ch, filterSQL(fineTable), "ops-gran-fine")
	if err != nil {
		log.Fatalf("run fine: %v", err)
	}
	coarseStat, err := runCountWithReadRows(ctx, ch, filterSQL(coarseTable), "ops-gran-coarse")
	if err != nil {
		log.Fatalf("run coarse: %v", err)
	}

	fmt.Printf("[granularity] fine (index_granularity=128):   count()=%d, read_rows=%d (из %d total)\n", fineStat.count, fineStat.readRows, fineRows)
	fmt.Printf("[granularity] coarse (index_granularity=8192): count()=%d, read_rows=%d (из %d total)\n", coarseStat.count, coarseStat.readRows, coarseRows)

	assertFailFast(fineRows == coarseRows, "одинаковое число строк fine/coarse: %d == %d", fineRows, coarseRows)
	assertFailFast(fineStat.count == coarseStat.count, "результат запроса не зависит от granularity: count() %d == %d", fineStat.count, coarseStat.count)
	assertFailFast(fineStat.readRows <= coarseStat.readRows, "более мелкая гранула читает не больше строк на узком фильтре: read_rows fine (%d) <= coarse (%d)", fineStat.readRows, coarseStat.readRows)
}

type countStat struct {
	count    uint64
	readRows uint64
}

// runCountWithReadRows — тот же паттерн, что ../go/mergetree/granules.go
// runWithReadRows, но для простого SELECT count() (без sum(revenue)).
func runCountWithReadRows(ctx context.Context, conn clickhouse.Conn, sql, queryIDPrefix string) (countStat, error) {
	queryID := fmt.Sprintf("%s-%d", queryIDPrefix, time.Now().UnixNano())
	qctx := clickhouse.Context(ctx, clickhouse.WithQueryID(queryID))
	var cnt uint64
	if err := conn.QueryRow(qctx, sql).Scan(&cnt); err != nil {
		return countStat{}, fmt.Errorf("run query: %w", err)
	}
	if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		return countStat{}, fmt.Errorf("flush logs: %w", err)
	}
	var readRows uint64
	err := conn.QueryRow(ctx, `
		SELECT read_rows FROM system.query_log
		WHERE query_id = ? AND type = 'QueryFinish'
		ORDER BY event_time DESC LIMIT 1`, queryID).Scan(&readRows)
	if err != nil {
		return countStat{count: cnt}, fmt.Errorf("read read_rows: %w", err)
	}
	return countStat{count: cnt, readRows: readRows}, nil
}

// phaseThreadsMemory — бонус Step 4 брифа: max_threads (host-зависимая
// латентность, информационно) и лимит памяти запроса (max_memory_usage —
// реальное отклонение сервером тяжёлого запроса, а не просто настройка на
// бумаге).
func phaseThreadsMemory(ctx context.Context, ch clickhouse.Conn, table string) {
	fmt.Println("\n=== max_threads / лимит памяти запроса — бонус Step 4 брифа ===")

	qctx1 := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"max_threads": 1}))
	start := time.Now()
	var cnt1 uint64
	if err := ch.QueryRow(qctx1, fmt.Sprintf("SELECT count() FROM demo.%s WHERE duration_ms > 1000", table)).Scan(&cnt1); err != nil {
		log.Fatalf("max_threads=1 query: %v", err)
	}
	dur1 := time.Since(start)

	qctxN := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"max_threads": 4}))
	start = time.Now()
	var cntN uint64
	if err := ch.QueryRow(qctxN, fmt.Sprintf("SELECT count() FROM demo.%s WHERE duration_ms > 1000", table)).Scan(&cntN); err != nil {
		log.Fatalf("max_threads=4 query: %v", err)
	}
	durN := time.Since(start)

	fmt.Printf("[threads] max_threads=1: count()=%d за %s\n", cnt1, dur1)
	fmt.Printf("[threads] max_threads=4: count()=%d за %s (host-зависимо — «характерный прогон», не абсолют)\n", cntN, durN)
	assertFailFast(cnt1 == cntN, "результат не зависит от max_threads: count() %d == %d", cnt1, cntN)

	fmt.Println("[memory] max_memory_usage=1000000 (1MB) на тяжёлом запросе (groupArray(url) по country) — ожидаем отказ сервера")
	qctxMem := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"max_memory_usage": 1_000_000}))
	heavySQL := fmt.Sprintf("SELECT country, groupArray(url) FROM demo.%s GROUP BY country", table)
	memErr := drainQuery(qctxMem, ch, heavySQL)
	if memErr == nil {
		fmt.Println("[memory] запрос неожиданно прошёл без ошибки лимита памяти — честно фиксируем как есть (не подгоняем вывод)")
	} else {
		fmt.Printf("[memory] запрос отклонён сервером, как и ожидалось при max_memory_usage=1MB: %v\n", memErr)
	}
	assertFailFast(memErr != nil, "max_memory_usage=1MB на тяжёлом groupArray-запросе приводит к отказу сервера (Memory limit exceeded) — лимит реально работает, а не просто настройка на бумаге")
}

func drainQuery(ctx context.Context, conn clickhouse.Conn, sql string) error {
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}
