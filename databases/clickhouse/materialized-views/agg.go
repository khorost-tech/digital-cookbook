package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/shopspring/decimal"
)

// phaseAgg — Step 1 брифа: MV как ТРИГГЕР на вставку.
//
//  1. Создаёт mv_events (пустая), вставляет небольшую "pre-MV" пачку.
//  2. Создаёт mv_events_agg + MV mv_events_to_agg_mv (только теперь).
//  3. Проверяет, что mv_events_agg ПУСТА, несмотря на уже существующие в
//     mv_events строки — MV не обрабатывает то, что было вставлено ДО её
//     создания (content-note брифа, здесь не утверждение, а измеренный факт).
//  4. TRUNCATE mv_events (чистое состояние для честного сравнения ниже) и
//     батч-загрузка основного датасета — теперь MV срабатывает на каждую
//     вставку.
//  5. Сверка предагрегата (-Merge / finalizeAggregation) с прямым
//     пересчётом по сырым данным.
func phaseAgg(ctx context.Context, ch clickhouse.Conn, csvPath string, preMVRows int, bulkBatch int, expectRows int64) {
	fmt.Println("\n=== MV КАК ТРИГГЕР НА ВСТАВКУ (Step 1 брифа) ===")

	mustRun(func() error { return createRawTable(ctx, ch, tblEvents) })

	preRows, _, err := batchLoad(ctx, ch, tblEvents, csvPath, preMVRows, int64(preMVRows))
	if err != nil {
		log.Fatalf("pre-MV load: %v", err)
	}
	fmt.Printf("[agg] pre-MV: %d rows вставлено в demo.%s ДО создания MV\n", preRows, tblEvents)
	assertFailFast(int64(preRows) == int64(preMVRows), "pre-MV rows loaded (%d) == -pre-mv-rows (%d)", preRows, preMVRows)

	mustRun(func() error { return createAggTable(ctx, ch, tblEventsAgg) })
	mustRun(func() error { return createAggMV(ctx, ch, mvEventsToAgg, tblEventsAgg, tblEvents) })
	fmt.Printf("[agg] создан demo.%s (AggregatingMergeTree) + demo.%s (MV, TO demo.%s FROM demo.%s)\n",
		tblEventsAgg, mvEventsToAgg, tblEventsAgg, tblEvents)

	aggRowsAfterCreate, err := activeParts(ctx, ch, tblEventsAgg)
	if err != nil {
		log.Fatalf("agg parts after create: %v", err)
	}
	aggCountAfterCreate, err := countRows(ctx, ch, tblEventsAgg)
	if err != nil {
		log.Fatalf("agg count after create: %v", err)
	}
	fmt.Printf("[agg] СРАЗУ после создания MV: demo.%s — %d active parts, count()=%d (несмотря на %d уже существующих строк в demo.%s)\n",
		tblEventsAgg, aggRowsAfterCreate, aggCountAfterCreate, preRows, tblEvents)
	assertFailFast(aggCountAfterCreate == 0, "content-note: MV НЕ обрабатывает существующие данные — demo.%s count() == 0 сразу после CREATE MATERIALIZED VIEW, несмотря на %d строк, уже лежащих в demo.%s", tblEventsAgg, preRows, tblEvents)

	if err := ch.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE demo.%s", tblEvents)); err != nil {
		log.Fatalf("truncate %s: %v", tblEvents, err)
	}
	fmt.Printf("[agg] demo.%s очищена (TRUNCATE) — дальше загружаем основной датасет ПОСЛЕ создания MV, для честного сравнения agg vs пересчёт\n", tblEvents)

	rows, dur, err := batchLoad(ctx, ch, tblEvents, csvPath, bulkBatch, 0)
	if err != nil {
		log.Fatalf("bulk load: %v", err)
	}
	fmt.Printf("[agg] bulk load: %d rows в demo.%s за %s (%.0f rows/s)\n", rows, tblEvents, dur, float64(rows)/dur.Seconds())
	if expectRows > 0 {
		assertFailFast(int64(rows) == expectRows, "bulk loaded rows (%d) == expected (-expect-rows=%d)", rows, expectRows)
	}

	aggRowsBeforeOptimize, err := countRows(ctx, ch, tblEventsAgg)
	if err != nil {
		log.Fatalf("agg count before optimize: %v", err)
	}
	fmt.Printf("[agg] demo.%s: count()=%d СРАЗУ после bulk load (много незамерженных partial-state строк на один ключ (country,hour) — по одной на каждый вставленный батч блока данных, ждущих фонового merge)\n", tblEventsAgg, aggRowsBeforeOptimize)

	verifyAggregate(ctx, ch, "до OPTIMIZE FINAL")

	if err := ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE demo.%s FINAL", tblEventsAgg)); err != nil {
		log.Fatalf("optimize final %s: %v", tblEventsAgg, err)
	}
	aggRowsAfterOptimize, err := countRows(ctx, ch, tblEventsAgg)
	if err != nil {
		log.Fatalf("agg count after optimize: %v", err)
	}
	fmt.Printf("[agg] demo.%s: count()=%d ПОСЛЕ OPTIMIZE TABLE ... FINAL (state-строки схлопнуты в одну на ключ (country,hour) — теперь finalizeAggregation() без GROUP BY тоже даёт корректный результат)\n", tblEventsAgg, aggRowsAfterOptimize)
	assertFailFast(aggRowsAfterOptimize <= aggRowsBeforeOptimize, "OPTIMIZE FINAL не увеличивает число строк: после (%d) <= до (%d)", aggRowsAfterOptimize, aggRowsBeforeOptimize)

	verifyAggregate(ctx, ch, "после OPTIMIZE FINAL")
	verifyFinalizeAggregationDirect(ctx, ch)
}

type aggBucket struct {
	country string
	hour    string // строкой (RFC-подобный вывод CH DateTime) — только для стабильного ключа сравнения
	revenue decimal.Decimal
	users   uint64
	cnt     uint64
}

// mergeQuerySQL/recomputeQuerySQL — единый шаблон, source-таблица/agg-таблица
// подставляются параметром (переиспользуется для основного mv_events_agg и
// для потокового mv_events_kafka_agg, Step 3).
const mergeQuerySQLTemplate = `
SELECT country, hour, sumMerge(revenue_state) AS revenue, uniqMerge(users_state) AS users, countMerge(count_state) AS cnt
FROM demo.%s
GROUP BY country, hour
ORDER BY country, hour
`

const recomputeQuerySQLTemplate = `
SELECT country, toStartOfHour(event_time) AS hour, sum(revenue) AS revenue, uniqExact(user_id) AS users, count() AS cnt
FROM demo.%s
GROUP BY country, hour
ORDER BY country, hour
`

func queryAggBuckets(ctx context.Context, ch clickhouse.Conn, sql string) ([]aggBucket, error) {
	rows, err := ch.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []aggBucket
	for rows.Next() {
		var b aggBucket
		var hour time.Time
		if err := rows.Scan(&b.country, &hour, &b.revenue, &b.users, &b.cnt); err != nil {
			return nil, err
		}
		b.hour = hour.UTC().Format(csvTimeLayout)
		out = append(out, b)
	}
	return out, rows.Err()
}

// verifyAggregate — сверка demo.mv_events_agg (через -Merge, Step 1 брифа)
// с прямым пересчётом по demo.mv_events. label — для сообщений (до/после
// OPTIMIZE FINAL; -Merge с GROUP BY корректен в ОБОИХ случаях, в отличие от
// "голого" finalizeAggregation, см. verifyFinalizeAggregationDirect).
func verifyAggregate(ctx context.Context, ch clickhouse.Conn, label string) {
	compareAggVsRecompute(ctx, ch, tblEventsAgg, tblEvents, label)
}

// compareAggVsRecompute — общая сверка agg (-Merge/GROUP BY) vs прямой
// пересчёт (sum/uniqExact/count по сырым), переиспользуется Step 1 и Step 3
// (Kafka-предагрегат).
func compareAggVsRecompute(ctx context.Context, ch clickhouse.Conn, aggTable, rawTable, label string) {
	merged, err := queryAggBuckets(ctx, ch, fmt.Sprintf(mergeQuerySQLTemplate, aggTable))
	if err != nil {
		log.Fatalf("query merged agg (%s): %v", label, err)
	}
	recomputed, err := queryAggBuckets(ctx, ch, fmt.Sprintf(recomputeQuerySQLTemplate, rawTable))
	if err != nil {
		log.Fatalf("query recompute (%s): %v", label, err)
	}
	fmt.Printf("[agg-verify %s] demo.%s (-Merge): %d групп (country,hour); прямой пересчёт по demo.%s: %d групп\n",
		label, aggTable, len(merged), rawTable, len(recomputed))

	recomputeIdx := map[string]aggBucket{}
	for _, b := range recomputed {
		recomputeIdx[b.country+"|"+b.hour] = b
	}

	var totalRevenueMerged, totalRevenueRecomputed decimal.Decimal
	var totalCntMerged, totalCntRecomputed uint64
	var absUserErr, totalExactUsers float64
	for _, m := range merged {
		key := m.country + "|" + m.hour
		r, ok := recomputeIdx[key]
		if !ok {
			log.Fatalf("[agg-verify %s] FAIL: группа (%s, %s) есть в agg, но отсутствует в пересчёте", label, m.country, m.hour)
		}
		totalRevenueMerged = totalRevenueMerged.Add(m.revenue)
		totalRevenueRecomputed = totalRevenueRecomputed.Add(r.revenue)
		totalCntMerged += m.cnt
		totalCntRecomputed += r.cnt
		absUserErr += math.Abs(float64(m.users) - float64(r.users))
		totalExactUsers += float64(r.users)
	}

	assertFailFast(len(merged) == len(recomputed), "[agg-verify %s] число групп (country,hour) совпадает: agg=%d, пересчёт=%d", label, len(merged), len(recomputed))
	assertFailFast(totalCntMerged == totalCntRecomputed, "[agg-verify %s] countMerge()==count(): agg total=%d, пересчёт total=%d", label, totalCntMerged, totalCntRecomputed)
	assertFailFast(totalRevenueMerged.Equal(totalRevenueRecomputed), "[agg-verify %s] sumMerge(revenue)==sum(revenue): agg total=%s, пересчёт total=%s", label, totalRevenueMerged.String(), totalRevenueRecomputed.String())

	userErrPct := 0.0
	if totalExactUsers > 0 {
		userErrPct = 100 * absUserErr / totalExactUsers
	}
	fmt.Printf("[agg-verify %s] uniqMerge(user_id) vs uniqExact(user_id): суммарная относительная погрешность %.4f%% (uniq — приближённый кардинальный оценщик HyperLogLog/linear-counting, а НЕ точный подсчёт — честная оговорка, не баг)\n", label, userErrPct)
	assertFailFast(userErrPct < 2.0, "[agg-verify %s] uniq() approximation в пределах ожидаемой погрешности: %.4f%% < 2.0%%", label, userErrPct)
}

// verifyFinalizeAggregationDirect — демонстрирует ГРАНИЦУ finalizeAggregation()
// без GROUP BY: функция финализирует состояние КАЖДОЙ строки САМОЙ ПО СЕБЕ,
// не объединяя строки с одинаковым ключом (country,hour), которые ещё не
// схлопнулись фоновым merge/OPTIMIZE FINAL в одну. После OPTIMIZE TABLE ...
// FINAL (уже выполненного в phaseAgg) в demo.mv_events_agg остаётся ровно
// одна строка на ключ — тогда finalizeAggregation() без GROUP BY даёт тот же
// результат, что -Merge с GROUP BY. Это explicit content-note брифа ("Запрос
// предагрегата через -Merge/finalizeAggregation").
func verifyFinalizeAggregationDirect(ctx context.Context, ch clickhouse.Conn) {
	rows, err := ch.Query(ctx, fmt.Sprintf(`
		SELECT country, hour, finalizeAggregation(revenue_state) AS revenue, finalizeAggregation(count_state) AS cnt
		FROM demo.%s
		ORDER BY country, hour
	`, tblEventsAgg))
	if err != nil {
		log.Fatalf("finalizeAggregation query: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var country string
		var hour time.Time
		var revenue decimal.Decimal
		var cnt uint64
		if err := rows.Scan(&country, &hour, &revenue, &cnt); err != nil {
			log.Fatalf("finalizeAggregation scan: %v", err)
		}
		n++
	}
	if rows.Err() != nil {
		log.Fatalf("finalizeAggregation rows: %v", rows.Err())
	}

	distinctKeys, err := countRows(ctx, ch, tblEventsAgg)
	if err != nil {
		log.Fatalf("count distinct keys check: %v", err)
	}
	fmt.Printf("[agg-verify finalizeAggregation] demo.%s ПОСЛЕ OPTIMIZE FINAL: finalizeAggregation() без GROUP BY вернул %d строк, count() таблицы = %d — совпадают (ровно одна state-строка на ключ после FINAL, finalizeAggregation() per-row эквивалентен -Merge)\n",
		tblEventsAgg, n, distinctKeys)
	assertFailFast(uint64(n) == distinctKeys, "finalizeAggregation() без GROUP BY после OPTIMIZE FINAL вернул ровно count()=%d строк (одна на ключ): got %d", distinctKeys, n)
}
