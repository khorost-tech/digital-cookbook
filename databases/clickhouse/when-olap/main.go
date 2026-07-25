// Command when-olap — стенд #1 серии "ClickHouse: глубокое погружение":
// когда OLAP (ClickHouse), а когда обычная OLTP-таблица PostgreSQL
// достаточна/предпочтительна. Один общий датасет (../dataset/main.go)
// загружается в обе СУБД, дальше сравниваются:
//
//   - размер на диске (CH system.parts vs PG pg_total_relation_size)
//   - один аналитический агрегат по всем строкам (GROUP BY country+сутки) —
//     с индексом и без индекса на стороне PG, EXPLAIN ANALYZE на обеих
//   - точечный SELECT по PK/суррогатному id (PG индекс выигрывает)
//   - точечный UPDATE/DELETE (PG мгновенно, CH — асинхронная мутация)
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cookbook-net, см.
// ../ops/when-olap-demo.sh за полный live-сценарий):
//
//	docker run --rm --network clickhouse-cookbook-net \
//	  -v "$(pwd)/when-olap:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=all -csv=/data/events.csv \
//	  -ch-addr=clickhouse:9000 -pg-dsn="postgres://postgres:chdemo@postgres:5432/demo"
//
// -phase=all прогоняет весь сценарий последовательно (schema → load → size →
// aggregate → point → mutate) с ассертами fail-loud на каждом шаге.
// Отдельные фазы (schema/load/size/aggregate/point/mutate) существуют для
// итеративной отладки и вызываются так же с тем же -csv (кроме schema).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	phase := flag.String("phase", "all", "schema|load|size|aggregate|point|mutate|all")
	chAddr := flag.String("ch-addr", "clickhouse:9000", "ClickHouse native addr")
	pgDSN := flag.String("pg-dsn", "postgres://postgres:chdemo@postgres:5432/demo", "PostgreSQL DSN")
	csvPath := flag.String("csv", "/data/events.csv", "путь к CSV-датасету (dataset/main.go)")
	batchSize := flag.Int("ch-batch", 1_000_000, "размер батча вставки в CH (строк)")
	rows := flag.Int64("expect-rows", 0, "если >0, ассерт: число строк в датасете/СУБД == это значение")
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

	switch *phase {
	case "schema":
		mustRun(func() error { return createSchema(ctx, ch, pgPool) })
	case "load":
		phaseLoad(ctx, ch, pgPool, *csvPath, *batchSize, *rows)
	case "size":
		phaseSize(ctx, ch, pgPool, *rows)
	case "aggregate":
		phaseAggregate(ctx, ch, pgPool, *runs)
	case "point":
		phasePoint(ctx, ch, pgPool, *rows)
	case "mutate":
		phaseMutate(ctx, ch, pgPool, *rows)
	case "all":
		mustRun(func() error { return createSchema(ctx, ch, pgPool) })
		phaseLoad(ctx, ch, pgPool, *csvPath, *batchSize, *rows)
		phaseSize(ctx, ch, pgPool, *rows)
		phaseAggregate(ctx, ch, pgPool, *runs)
		phasePoint(ctx, ch, pgPool, *rows)
		phaseMutate(ctx, ch, pgPool, *rows)
		fmt.Println("\n[when-olap] all phases completed, all asserts passed")
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

func phaseLoad(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool, csvPath string, batchSize int, expectRows int64) {
	fmt.Println("\n=== LOAD ===")
	chRows, chDur, err := loadClickHouse(ctx, ch, csvPath, batchSize)
	if err != nil {
		log.Fatalf("load ch: %v", err)
	}
	fmt.Printf("[load] CH: %d rows in %s (%.0f rows/s)\n", chRows, chDur, float64(chRows)/chDur.Seconds())

	pgRows, pgDur, err := loadPostgres(ctx, pg, csvPath)
	if err != nil {
		log.Fatalf("load pg: %v", err)
	}
	fmt.Printf("[load] PG: %d rows in %s (%.0f rows/s)\n", pgRows, pgDur, float64(pgRows)/pgDur.Seconds())

	assertFailFast(chRows == uint64(pgRows), "CH rows (%d) == PG rows (%d)", chRows, pgRows)
	if expectRows > 0 {
		assertFailFast(int64(chRows) == expectRows, "loaded rows (%d) == expected (-expect-rows=%d)", chRows, expectRows)
	}
}

func phaseSize(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool, expectRows int64) {
	fmt.Println("\n=== SIZE ON DISK ===")
	sr, err := measureSize(ctx, ch, pg)
	if err != nil {
		log.Fatalf("size: %v", err)
	}
	fmt.Printf("[size] CH: %s on disk, %d active parts, %d rows (system.parts)\n", humanBytes(int64(sr.chBytes)), sr.chParts, sr.chRows)
	fmt.Printf("[size] PG: %s total (%s table + %s indexes), %d rows\n",
		humanBytes(sr.pgBytes), humanBytes(sr.pgTable), humanBytes(sr.pgIdx), sr.pgRows)

	ratio := float64(sr.pgBytes) / float64(sr.chBytes)
	fmt.Printf("[size] compression ratio PG/CH = %.2fx\n", ratio)

	assertFailFast(sr.chRows == uint64(sr.pgRows), "CH rows (%d) == PG rows (%d) after load", sr.chRows, sr.pgRows)
	if expectRows > 0 {
		assertFailFast(int64(sr.pgRows) == expectRows, "row count (%d) == expected (-expect-rows=%d)", sr.pgRows, expectRows)
	}
	assertFailFast(ratio >= 2.0, "CH on-disk size multiple times smaller than PG: ratio %.2fx >= 2.0x (CH %s vs PG %s)",
		ratio, humanBytes(int64(sr.chBytes)), humanBytes(sr.pgBytes))
}

func phaseAggregate(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool, runs int) {
	fmt.Println("\n=== AGGREGATE QUERY (с индексами PG) ===")
	pgPlanWithIdx, err := explainPG(ctx, pg)
	if err != nil {
		log.Fatalf("pg explain with index: %v", err)
	}
	fmt.Println("[aggregate] PG EXPLAIN ANALYZE (с индексами event_time/country):")
	fmt.Println(pgPlanWithIdx)

	chPlan, err := explainCH(ctx, ch)
	if err != nil {
		log.Fatalf("ch explain: %v", err)
	}
	fmt.Println("[aggregate] CH EXPLAIN:")
	fmt.Println(chPlan)

	chRuns, err := runMultipleCH(ctx, ch, runs)
	if err != nil {
		log.Fatalf("ch aggregate runs: %v", err)
	}
	pgRunsWithIdx, err := runMultiplePG(ctx, pg, runs)
	if err != nil {
		log.Fatalf("pg aggregate runs (with index): %v", err)
	}
	chMedian := medianDuration(chRuns)
	pgMedianWithIdx := medianDuration(pgRunsWithIdx)
	fmt.Printf("[aggregate] CH median over %d runs: %s (durations: %v)\n", runs, chMedian, durs(chRuns))
	fmt.Printf("[aggregate] PG (с индексами) median over %d runs: %s (durations: %v)\n", runs, pgMedianWithIdx, durs(pgRunsWithIdx))

	fmt.Println("\n=== AGGREGATE QUERY (без индексов PG — DROP idx_events_event_time/idx_events_country) ===")
	mustRun(func() error {
		_, err := pg.Exec(ctx, "DROP INDEX IF EXISTS idx_events_event_time")
		return err
	})
	mustRun(func() error {
		_, err := pg.Exec(ctx, "DROP INDEX IF EXISTS idx_events_country")
		return err
	})
	pgPlanNoIdx, err := explainPG(ctx, pg)
	if err != nil {
		log.Fatalf("pg explain without index: %v", err)
	}
	fmt.Println("[aggregate] PG EXPLAIN ANALYZE (без индексов):")
	fmt.Println(pgPlanNoIdx)
	pgRunsNoIdx, err := runMultiplePG(ctx, pg, runs)
	if err != nil {
		log.Fatalf("pg aggregate runs (no index): %v", err)
	}
	pgMedianNoIdx := medianDuration(pgRunsNoIdx)
	fmt.Printf("[aggregate] PG (без индексов) median over %d runs: %s (durations: %v)\n", runs, pgMedianNoIdx, durs(pgRunsNoIdx))

	// восстановить индексы — они нужны дальше для честного point-lookup
	// сравнения (Step 3 брифа опирается на PK, не на эти индексы, но
	// оставляем схему консистентной с README/Step 1 после прогона).
	mustRun(func() error {
		_, err := pg.Exec(ctx, pgIdxEventTime)
		return err
	})
	mustRun(func() error {
		_, err := pg.Exec(ctx, pgIdxCountry)
		return err
	})
	fmt.Println("[aggregate] PG indexes restored (idx_events_event_time, idx_events_country)")

	assertFailFast(chRuns[0].groups == pgRunsWithIdx[0].groups, "CH groups (%d) == PG groups (%d)", chRuns[0].groups, pgRunsWithIdx[0].groups)

	ratio := float64(pgMedianWithIdx) / float64(chMedian)
	assertFailFast(ratio >= 1.5, "CH aggregate multiple times faster than PG full-scan aggregate: ratio %.2fx >= 1.5x (CH %s vs PG %s)",
		ratio, chMedian, pgMedianWithIdx)
}

func durs(rs []aggResult) []time.Duration {
	out := make([]time.Duration, len(rs))
	for i, r := range rs {
		out[i] = r.duration
	}
	return out
}

func phasePoint(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool, totalRows int64) {
	fmt.Println("\n=== ТОЧЕЧНЫЙ SELECT ПО PK/id ===")
	id := uint64(totalRows / 2)
	if id == 0 {
		id = 1
	}

	pgRes, pgPlan, err := pointSelectPG(ctx, pg, id)
	if err != nil {
		log.Fatalf("pg point select: %v", err)
	}
	fmt.Printf("[point] PG SELECT ... WHERE id=%d: %s (found=%v)\n", id, pgRes.duration, pgRes.found)
	fmt.Println("[point] PG EXPLAIN ANALYZE:")
	fmt.Println(pgPlan)

	chDur, chFound, readRows, err := pointSelectCH(ctx, ch, id)
	if err != nil {
		log.Fatalf("ch point select: %v", err)
	}
	fmt.Printf("[point] CH SELECT ... WHERE id=%d: %s (found=%v, read_rows=%d)\n", id, chDur, chFound, readRows)

	assertFailFast(pgRes.found && chFound, "point lookup found row on both engines (pg=%v ch=%v)", pgRes.found, chFound)
	if readRows > 0 {
		assertFailFast(readRows > uint64(totalRows)/10, "CH point select by non-sort-key column scans a large share of the table: read_rows=%d out of %d total", readRows, totalRows)
	}
	usesIndex := strings.Contains(pgPlan, "Index Scan") || strings.Contains(pgPlan, "Index Only Scan") || strings.Contains(pgPlan, "Bitmap")
	assertFailFast(usesIndex, "PG point select uses an index scan for PK lookup (plan contains Index Scan/Index Only Scan/Bitmap)")
}

func phaseMutate(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool, totalRows int64) {
	fmt.Println("\n=== ТОЧЕЧНЫЙ UPDATE/DELETE — где CH противопоказан ===")
	updID := uint64(totalRows/4 + 1)
	delID := uint64(totalRows/4 + 2)

	pgUpdDur, err := pgMutateUpdate(ctx, pg, updID)
	if err != nil {
		log.Fatalf("pg update: %v", err)
	}
	fmt.Printf("[mutate] PG UPDATE ... WHERE id=%d: %s (синхронно, мгновенно)\n", updID, pgUpdDur)

	pgDelDur, err := pgMutateDelete(ctx, pg, delID)
	if err != nil {
		log.Fatalf("pg delete: %v", err)
	}
	fmt.Printf("[mutate] PG DELETE ... WHERE id=%d: %s (синхронно, мгновенно)\n", delID, pgDelDur)

	chUpd, err := chMutateUpdate(ctx, ch, updID)
	if err != nil {
		log.Fatalf("ch update mutation: %v", err)
	}
	fmt.Printf("[mutate] CH ALTER ... UPDATE WHERE id=%d: submit=%s (выглядит мгновенно), completion=%s (parts_to_do at submit=%d) — РЕАЛЬНАЯ стоимость\n",
		updID, chUpd.submitDuration, chUpd.completionDuration, chUpd.partsToDoAtSubmit)

	chDel, err := chMutateDelete(ctx, ch, delID)
	if err != nil {
		log.Fatalf("ch delete mutation: %v", err)
	}
	fmt.Printf("[mutate] CH ALTER ... DELETE WHERE id=%d: submit=%s (выглядит мгновенно), completion=%s (parts_to_do at submit=%d) — РЕАЛЬНАЯ стоимость\n",
		delID, chDel.submitDuration, chDel.completionDuration, chDel.partsToDoAtSubmit)

	assertFailFast(pgUpdDur < 500*time.Millisecond, "PG point UPDATE completes in sub-second time: %s < 500ms", pgUpdDur)
	assertFailFast(pgDelDur < 500*time.Millisecond, "PG point DELETE completes in sub-second time: %s < 500ms", pgDelDur)
	assertFailFast(chUpd.completionDuration > pgUpdDur, "CH mutation completion (%s) costs materially more than PG point UPDATE (%s)", chUpd.completionDuration, pgUpdDur)
	assertFailFast(chDel.completionDuration > pgDelDur, "CH mutation completion (%s) costs materially more than PG point DELETE (%s)", chDel.completionDuration, pgDelDur)
}
