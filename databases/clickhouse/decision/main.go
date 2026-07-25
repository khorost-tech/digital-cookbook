// Command decision — стенд #8 (финальный) серии "ClickHouse: глубокое
// погружение": карта выбора ClickHouse vs TimescaleDB vs DuckDB vs
// PostgreSQL. Один и тот же аналитический сценарий (тот же датасет/агрегат,
// что стенд #1 when-olap: GROUP BY country + сутки, count()+sum(revenue))
// на четырёх системах:
//
//   - ClickHouse (MergeTree) — специализированный OLAP-движок, кластер/сервис
//   - TimescaleDB (hypertable + time_bucket + continuous aggregate +
//     native compression) — расширение PostgreSQL, обзорно (глубокий
//     time-series — отдельная будущая статья, здесь не дублируется)
//   - DuckDB (embedded, через go-duckdb/CGO, БЕЗ отдельного сервера) —
//     запрос датасета напрямую из Parquet
//   - PostgreSQL (обычная таблица, без индексов) — baseline
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../ops/decision-demo.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/decision:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=all -csv=/data/events-decision.csv \
//	  -ch-addr=clickhouse:9000 \
//	  -pg-dsn="postgres://postgres:chdemo@postgres:5432/demo" \
//	  -ts-dsn="postgres://postgres:chdemo@timescaledb:5432/demo"
//
// -phase=all прогоняет весь сценарий последовательно (schema → load → size →
// aggregate → timescale) с ассертами fail-loud на каждом шаге.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	phase := flag.String("phase", "all", "schema|load|size|aggregate|timescale|all")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr")
	pgDSN := flag.String("pg-dsn", "postgres://postgres:chdemo@postgres:5432/demo", "PostgreSQL (baseline) DSN")
	tsDSN := flag.String("ts-dsn", "postgres://postgres:chdemo@timescaledb:5432/demo", "TimescaleDB DSN")
	csvPath := flag.String("csv", "/data/events-decision.csv", "путь к общему CSV-датасету (усечённое подмножество dataset/main.go -seed=42, общее для всех 4 систем)")
	parquetPath := flag.String("parquet", "/data/events-decision.parquet", "путь к сконвертированному Parquet (DuckDB)")
	chBatch := flag.Int("ch-batch", 1_000_000, "размер батча вставки в CH (строк)")
	expectRows := flag.Int64("expect-rows", 0, "если >0, ассерт: число загруженных строк на каждой из 4 систем == это значение")
	runs := flag.Int("runs", 5, "число прогонов аналитического запроса для медианы")
	flag.Parse()

	ctx := context.Background()

	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{*chAddr},
		Auth:        clickhouse.Auth{Database: "demo", Username: "default"},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		log.Fatalf("ch open: %v", err)
	}
	defer ch.Close()
	if err := ch.Ping(ctx); err != nil {
		log.Fatalf("ch ping: %v", err)
	}

	pgPool, err := pgxpool.New(ctx, *pgDSN)
	if err != nil {
		log.Fatalf("pg pool: %v", err)
	}
	defer pgPool.Close()
	if err := pgPool.Ping(ctx); err != nil {
		log.Fatalf("pg ping: %v", err)
	}

	tsPool, err := pgxpool.New(ctx, *tsDSN)
	if err != nil {
		log.Fatalf("ts pool: %v", err)
	}
	defer tsPool.Close()
	if err := tsPool.Ping(ctx); err != nil {
		log.Fatalf("ts ping: %v", err)
	}

	switch *phase {
	case "schema":
		mustRun(func() error { return createSchema(ctx, ch, pgPool, tsPool) })
	case "load":
		phaseLoad(ctx, ch, pgPool, tsPool, *csvPath, *parquetPath, *chBatch, *expectRows)
	case "size":
		phaseSize(ctx, ch, pgPool, tsPool, *parquetPath, *expectRows)
	case "aggregate":
		phaseAggregate(ctx, ch, pgPool, tsPool, *parquetPath, *runs)
	case "timescale":
		phaseTimescale(ctx, tsPool, *runs)
	case "all":
		mustRun(func() error { return createSchema(ctx, ch, pgPool, tsPool) })
		phaseLoad(ctx, ch, pgPool, tsPool, *csvPath, *parquetPath, *chBatch, *expectRows)
		phaseSize(ctx, ch, pgPool, tsPool, *parquetPath, *expectRows)
		phaseAggregate(ctx, ch, pgPool, tsPool, *parquetPath, *runs)
		phaseTimescale(ctx, tsPool, *runs)
		fmt.Println("\n[decision] all phases completed, all asserts passed")
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

func phaseLoad(ctx context.Context, ch clickhouse.Conn, pg, ts *pgxpool.Pool, csvPath, parquetPath string, batchSize int, expectRows int64) {
	fmt.Println("\n=== LOAD (4 системы, один и тот же CSV) ===")

	chRows, chDur, err := loadClickHouse(ctx, ch, csvPath, batchSize)
	if err != nil {
		log.Fatalf("load ch: %v", err)
	}
	fmt.Printf("[load] CH:        %d rows in %s (%.0f rows/s)\n", chRows, chDur, float64(chRows)/chDur.Seconds())

	pgRows, pgDur, err := loadPGLike(ctx, pg, csvPath)
	if err != nil {
		log.Fatalf("load pg: %v", err)
	}
	fmt.Printf("[load] PG:        %d rows in %s (%.0f rows/s)\n", pgRows, pgDur, float64(pgRows)/pgDur.Seconds())

	tsRows, tsDur, err := loadPGLike(ctx, ts, csvPath)
	if err != nil {
		log.Fatalf("load timescale: %v", err)
	}
	fmt.Printf("[load] Timescale: %d rows in %s (%.0f rows/s)\n", tsRows, tsDur, float64(tsRows)/tsDur.Seconds())

	duckRows, duckDur, err := duckdbConvertCSVToParquet(csvPath, parquetPath)
	if err != nil {
		log.Fatalf("load duckdb (csv->parquet convert): %v", err)
	}
	fmt.Printf("[load] DuckDB:    %d rows converted CSV->Parquet in %s (%.0f rows/s) — эквивалент \"загрузки\" (нет сервера, есть файл-артефакт)\n",
		duckRows, duckDur, float64(duckRows)/duckDur.Seconds())

	assertFailFast(chRows == uint64(pgRows), "CH rows (%d) == PG rows (%d)", chRows, pgRows)
	assertFailFast(chRows == uint64(tsRows), "CH rows (%d) == Timescale rows (%d)", chRows, tsRows)
	assertFailFast(chRows == uint64(duckRows), "CH rows (%d) == DuckDB/Parquet rows (%d)", chRows, duckRows)
	if expectRows > 0 {
		assertFailFast(int64(chRows) == expectRows, "loaded rows (%d) == expected (-expect-rows=%d)", chRows, expectRows)
	}
}

func phaseSize(ctx context.Context, ch clickhouse.Conn, pg, ts *pgxpool.Pool, parquetPath string, expectRows int64) {
	fmt.Println("\n=== SIZE ON DISK (4 системы, ДО TimescaleDB-компрессии) ===")

	chBytes, chParts, chRows, err := sizeCH(ctx, ch)
	if err != nil {
		log.Fatalf("size ch: %v", err)
	}
	fmt.Printf("[size] CH:        %s on disk, %d active parts, %d rows (system.parts)\n", humanBytes(int64(chBytes)), chParts, chRows)

	pgBytes, pgRows, err := sizePGLike(ctx, pg)
	if err != nil {
		log.Fatalf("size pg: %v", err)
	}
	fmt.Printf("[size] PG:        %s total (pg_total_relation_size), %d rows\n", humanBytes(pgBytes), pgRows)

	tsBytesBefore, err := sizeHypertable(ctx, ts)
	if err != nil {
		log.Fatalf("size timescale: %v", err)
	}
	tsRows, err := countDecisionEvents(ctx, ts)
	if err != nil {
		log.Fatalf("size timescale rows: %v", err)
	}
	fmt.Printf("[size] Timescale: %s total (hypertable_size, ДО compress_chunk), %d rows\n", humanBytes(tsBytesBefore), tsRows)

	duckBytes, err := duckdbParquetFileSize(parquetPath)
	if err != nil {
		log.Fatalf("size duckdb parquet: %v", err)
	}
	fmt.Printf("[size] DuckDB:    %s (единственный Parquet-файл на диске, никакого серверного хранилища)\n", humanBytes(duckBytes))

	fmt.Printf("[size] CH компактнее PG в %.2fx, компактнее Timescale(до компрессии) в %.2fx, компактнее Parquet(DuckDB, уже колоночный+сжатый) в %.2fx\n",
		float64(pgBytes)/float64(chBytes), float64(tsBytesBefore)/float64(chBytes), float64(duckBytes)/float64(chBytes))

	assertFailFast(chRows == uint64(pgRows), "CH rows (%d) == PG rows (%d)", chRows, pgRows)
	assertFailFast(chRows == uint64(tsRows), "CH rows (%d) == Timescale rows (%d)", chRows, tsRows)
	if expectRows > 0 {
		assertFailFast(int64(chRows) == expectRows, "row count (%d) == expected (-expect-rows=%d)", chRows, expectRows)
	}
}

func phaseAggregate(ctx context.Context, ch clickhouse.Conn, pg, ts *pgxpool.Pool, parquetPath string, runs int) {
	fmt.Println("\n=== AGGREGATE (один и тот же запрос, 4 системы, медиана по прогонам) ===")

	chMedian, chCS, chDurs, err := runMultiple(runs, func() (aggResult, checksumResult, error) { return runCHAggregate(ctx, ch) })
	if err != nil {
		log.Fatalf("ch aggregate: %v", err)
	}
	fmt.Printf("[aggregate] CH:        median over %d runs: %s (durations: %v)\n", runs, chMedian, chDurs)

	pgMedian, pgCS, pgDurs, err := runMultiple(runs, func() (aggResult, checksumResult, error) { return runPGLikeAggregate(ctx, pg) })
	if err != nil {
		log.Fatalf("pg aggregate: %v", err)
	}
	fmt.Printf("[aggregate] PG:        median over %d runs: %s (durations: %v)\n", runs, pgMedian, pgDurs)

	tsMedian, tsCS, tsDurs, err := runMultiple(runs, func() (aggResult, checksumResult, error) { return runPGLikeAggregate(ctx, ts) })
	if err != nil {
		log.Fatalf("timescale aggregate (raw hypertable): %v", err)
	}
	fmt.Printf("[aggregate] Timescale (сырая гипертаблица): median over %d runs: %s (durations: %v)\n", runs, tsMedian, tsDurs)

	duckMedian, duckCS, duckDurs, err := runDuckDBAggregateMultiple(runs, parquetPath)
	if err != nil {
		log.Fatalf("duckdb aggregate: %v", err)
	}
	fmt.Printf("[aggregate] DuckDB (read_parquet, embedded): median over %d runs: %s (durations: %v)\n", runs, duckMedian, duckDurs)

	fmt.Printf("[aggregate] checksums: CH=%08x(%d groups) PG=%08x(%d groups) Timescale=%08x(%d groups) DuckDB=%08x(%d groups)\n",
		chCS.checksum, chCS.groups, pgCS.checksum, pgCS.groups, tsCS.checksum, tsCS.groups, duckCS.checksum, duckCS.groups)

	assertFailFast(chCS.checksum == pgCS.checksum && chCS.groups == pgCS.groups && chCS.totalCents == pgCS.totalCents,
		"результат агрегата CH == PG (checksum/groups/totalCents побайтово)")
	assertFailFast(chCS.checksum == tsCS.checksum && chCS.groups == tsCS.groups && chCS.totalCents == tsCS.totalCents,
		"результат агрегата CH == TimescaleDB сырая гипертаблица (checksum/groups/totalCents побайтово)")
	assertFailFast(chCS.checksum == duckCS.checksum && chCS.groups == duckCS.groups && chCS.totalCents == duckCS.totalCents,
		"результат агрегата CH == DuckDB/Parquet (checksum/groups/totalCents побайтово)")

	fmt.Printf("[aggregate] все 4 системы дали ОДИНАКОВЫЙ результат агрегата: %d групп (country, сутки), totalCents=%d\n", chCS.groups, chCS.totalCents)

	ranking := []struct {
		name string
		d    time.Duration
	}{{"CH", chMedian}, {"PG", pgMedian}, {"Timescale(raw)", tsMedian}, {"DuckDB", duckMedian}}
	fmt.Println("[aggregate] относительные разы к самому быстрому (CH — специализированный OLAP, ожидаемо впереди):")
	for _, r := range ranking {
		fmt.Printf("  %-16s %10s  (%.2fx к CH)\n", r.name, r.d, float64(r.d)/float64(chMedian))
	}
}

// phaseTimescale — Step 2 брифа, обзорно: continuous aggregate
// (материализация + сверка с сырыми данными) + native compression (эффект
// на размер, повторный замер агрегата на сжатых данных).
func phaseTimescale(ctx context.Context, ts *pgxpool.Pool, runs int) {
	fmt.Println("\n=== TIMESCALEDB: continuous aggregate + compression (обзорно) ===")

	mustRun(func() error { return createContinuousAggregate(ctx, ts) })

	refreshDur, err := refreshContinuousAggregate(ctx, ts)
	if err != nil {
		log.Fatalf("ts refresh cagg: %v", err)
	}
	caggRows, err := caggRowCount(ctx, ts)
	if err != nil {
		log.Fatalf("ts cagg row count: %v", err)
	}
	fmt.Printf("[timescale] refresh_continuous_aggregate: %s, decision_events_daily теперь %d строк (country x сутки)\n", refreshDur, caggRows)

	caggMedian, caggCS, caggDurs, err := runMultiple(runs, func() (aggResult, checksumResult, error) { return runTimescaleCaggAggregate(ctx, ts) })
	if err != nil {
		log.Fatalf("ts cagg aggregate: %v", err)
	}
	fmt.Printf("[timescale] cagg query median over %d runs: %s (durations: %v)\n", runs, caggMedian, caggDurs)

	rawMedian, rawCS, _, err := runMultiple(runs, func() (aggResult, checksumResult, error) { return runPGLikeAggregate(ctx, ts) })
	if err != nil {
		log.Fatalf("ts raw aggregate (для сверки с cagg): %v", err)
	}

	assertFailFast(caggCS.checksum == rawCS.checksum && caggCS.groups == rawCS.groups && caggCS.totalCents == rawCS.totalCents,
		"continuous aggregate (%d групп) == пересчёт по сырой гипертаблице (%d групп), побайтово", caggCS.groups, rawCS.groups)
	fmt.Printf("[timescale] cagg %s vs сырой запрос %s — cagg быстрее в %.2fx (предагрегат уже материализован)\n",
		caggMedian, rawMedian, float64(rawMedian)/float64(caggMedian))

	chunksBefore, err := chunkCount(ctx, ts)
	if err != nil {
		log.Fatalf("ts chunk count before: %v", err)
	}
	sizeBefore, err := sizeHypertable(ctx, ts)
	if err != nil {
		log.Fatalf("ts size before compression: %v", err)
	}
	fmt.Printf("[timescale] ДО compression: %d chunks, %s (hypertable_size)\n", chunksBefore, humanBytes(sizeBefore))

	mustRun(func() error { return enableCompression(ctx, ts) })
	compressed, compressDur, err := compressAllChunks(ctx, ts)
	if err != nil {
		log.Fatalf("ts compress chunks: %v", err)
	}
	fmt.Printf("[timescale] compress_chunk: %d chunks сжато за %s\n", compressed, compressDur)

	sizeAfter, err := sizeHypertable(ctx, ts)
	if err != nil {
		log.Fatalf("ts size after compression: %v", err)
	}
	ratio := float64(sizeBefore) / float64(sizeAfter)
	fmt.Printf("[timescale] ПОСЛЕ compression: %s (hypertable_size) — компрессия дала %.2fx\n", humanBytes(sizeAfter), ratio)

	rowsAfterCompress, err := countDecisionEvents(ctx, ts)
	if err != nil {
		log.Fatalf("ts count after compression: %v", err)
	}
	compressedMedian, compressedCS, _, err := runMultiple(runs, func() (aggResult, checksumResult, error) { return runPGLikeAggregate(ctx, ts) })
	if err != nil {
		log.Fatalf("ts aggregate after compression: %v", err)
	}
	fmt.Printf("[timescale] агрегат ПОСЛЕ компрессии: median %s (было %s ДО компрессии), rows=%d\n", compressedMedian, rawMedian, rowsAfterCompress)

	assertFailFast(compressed == chunksBefore, "все чанки сжаты (compress_chunk обработал %d == show_chunks %d ДО компрессии)", compressed, chunksBefore)
	assertFailFast(sizeAfter < sizeBefore, "compression уменьшила размер гипертаблицы: after (%s) < before (%s)", humanBytes(sizeAfter), humanBytes(sizeBefore))
	assertFailFast(compressedCS.checksum == rawCS.checksum && compressedCS.groups == rawCS.groups && compressedCS.totalCents == rawCS.totalCents,
		"результат агрегата НЕ изменился после компрессии (checksum/groups/totalCents побайтово)")
}
