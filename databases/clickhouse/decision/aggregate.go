package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Один и тот же аналитический агрегат (буквально тот же запрос, что стенд
// #1 when-olap: GROUP BY country + сутки, count()+sum(revenue)) на четырёх
// системах. sum(revenue) сравнивается как ЦЕЛЫЕ ЦЕНТЫ (round(...*100) ->
// bigint), не как Decimal/NUMERIC/DOUBLE — тот же приём, что стенд #3
// (drivers) verify: убирает разницу в представлении дробных типов между
// движками из уравнения, сравнивается именно результат агрегата.

const chAggregateSQL = `
SELECT country, toDate(event_time) AS d, count() AS cnt, toInt64(round(sum(revenue) * 100)) AS cents
FROM demo.decision_events
GROUP BY country, d
ORDER BY country, d
`

// pgLikeAggregateSQL — идентичный текст для baseline PG И "сырой"
// TimescaleDB-гипертаблицы (event_time::date эквивалентен
// time_bucket('1 day', event_time) с дефолтным origin — оба выравнивают на
// границу UTC-суток, см. README).
const pgLikeAggregateSQL = `
SELECT country, event_time::date AS d, count(*) AS cnt, (round(sum(revenue) * 100))::bigint AS cents
FROM decision_events
GROUP BY country, d
ORDER BY country, d
`

// tsCaggAggregateSQL — тот же результат, но читается из continuous
// aggregate decision_events_daily (Step 2 брифа: сверка -Merge/предагрегата
// с пересчётом по сырым данным — тот же принцип, что стенд #4
// materialized-views).
const tsCaggAggregateSQL = `
SELECT country, day AS d, cnt, (round(rev * 100))::bigint AS cents
FROM decision_events_daily
ORDER BY country, d
`

func runCHAggregate(ctx context.Context, conn clickhouse.Conn) (aggResult, checksumResult, error) {
	start := time.Now()
	rows, err := conn.Query(ctx, chAggregateSQL)
	if err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("ch aggregate query: %w", err)
	}
	defer rows.Close()

	cs := newChecksum()
	var country string
	var d time.Time
	var cnt uint64
	var cents int64
	for rows.Next() {
		if err := rows.Scan(&country, &d, &cnt, &cents); err != nil {
			return aggResult{}, checksumResult{}, fmt.Errorf("ch aggregate scan: %w", err)
		}
		cs.add(country, d, int64(cnt), cents)
	}
	if err := rows.Err(); err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("ch aggregate rows err: %w", err)
	}
	return aggResult{duration: time.Since(start)}, cs.result(), nil
}

func runPGLikeAggregate(ctx context.Context, pool *pgxpool.Pool) (aggResult, checksumResult, error) {
	start := time.Now()
	rows, err := pool.Query(ctx, pgLikeAggregateSQL)
	if err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("pg-like aggregate query: %w", err)
	}
	defer rows.Close()

	cs := newChecksum()
	for rows.Next() {
		var country string
		var d time.Time
		var cnt int64
		var cents int64
		if err := rows.Scan(&country, &d, &cnt, &cents); err != nil {
			return aggResult{}, checksumResult{}, fmt.Errorf("pg-like aggregate scan: %w", err)
		}
		cs.add(country, d, cnt, cents)
	}
	if rows.Err() != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("pg-like aggregate rows err: %w", rows.Err())
	}
	return aggResult{duration: time.Since(start)}, cs.result(), nil
}

func runTimescaleCaggAggregate(ctx context.Context, pool *pgxpool.Pool) (aggResult, checksumResult, error) {
	start := time.Now()
	rows, err := pool.Query(ctx, tsCaggAggregateSQL)
	if err != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("ts cagg aggregate query: %w", err)
	}
	defer rows.Close()

	cs := newChecksum()
	for rows.Next() {
		var country string
		var d time.Time
		var cnt int64
		var cents int64
		if err := rows.Scan(&country, &d, &cnt, &cents); err != nil {
			return aggResult{}, checksumResult{}, fmt.Errorf("ts cagg aggregate scan: %w", err)
		}
		cs.add(country, d, cnt, cents)
	}
	if rows.Err() != nil {
		return aggResult{}, checksumResult{}, fmt.Errorf("ts cagg aggregate rows err: %w", rows.Err())
	}
	return aggResult{duration: time.Since(start)}, cs.result(), nil
}

// runMultiple прогоняет f n раз ("несколько прогонов, характерный" — из
// требования брифа), возвращает медиану длительности и checksum ПЕРВОГО
// прогона + ассерт, что checksum стабилен между прогонами (детерминизм
// агрегата, не просто скорость).
func runMultiple(n int, f func() (aggResult, checksumResult, error)) (time.Duration, checksumResult, []time.Duration, error) {
	durs := make([]time.Duration, 0, n)
	var first checksumResult
	for i := 0; i < n; i++ {
		r, cs, err := f()
		if err != nil {
			return 0, checksumResult{}, durs, err
		}
		durs = append(durs, r.duration)
		if i == 0 {
			first = cs
		} else {
			assertFailFast(cs.checksum == first.checksum && cs.groups == first.groups && cs.totalCents == first.totalCents,
				"checksum стабилен между прогонами %d/%d (детерминизм агрегата)", i+1, n)
		}
	}
	return medianDuration(durs), first, durs, nil
}

func medianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(ds))
	copy(sorted, ds)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	return sorted[len(sorted)/2]
}
