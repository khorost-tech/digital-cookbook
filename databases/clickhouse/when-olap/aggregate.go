package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// Один и тот же аналитический агрегат на обеих СУБД: полная агрегация по
// (почти) всем строкам — GROUP BY country + сутки, count()+sum(revenue).
// Это типичный OLAP-паттерн ("сколько выручки по странам по дням") — то,
// что PostgreSQL вынужден делать Seq Scan'ом по всей таблице (индексы на
// event_time/country НЕ помогают агрегату без узкого WHERE — см. README).
const chAggregateSQL = `
SELECT country, toDate(event_time) AS d, count() AS cnt, sum(revenue) AS rev
FROM demo.events
GROUP BY country, d
ORDER BY country, d
`

const pgAggregateSQL = `
SELECT country, event_time::date AS d, count(*) AS cnt, sum(revenue) AS rev
FROM events
GROUP BY country, d
ORDER BY country, d
`

type aggResult struct {
	groups   int
	duration time.Duration
}

func runCHAggregate(ctx context.Context, conn clickhouse.Conn) (aggResult, error) {
	start := time.Now()
	rows, err := conn.Query(ctx, chAggregateSQL)
	if err != nil {
		return aggResult{}, fmt.Errorf("ch aggregate query: %w", err)
	}
	defer rows.Close()

	var country string
	var d time.Time
	var cnt uint64
	var rev decimal.Decimal
	groups := 0
	for rows.Next() {
		if err := rows.Scan(&country, &d, &cnt, &rev); err != nil {
			return aggResult{}, fmt.Errorf("ch aggregate scan: %w", err)
		}
		groups++
	}
	if err := rows.Err(); err != nil {
		return aggResult{}, fmt.Errorf("ch aggregate rows err: %w", err)
	}
	return aggResult{groups: groups, duration: time.Since(start)}, nil
}

func runPGAggregate(ctx context.Context, pool *pgxpool.Pool) (aggResult, error) {
	start := time.Now()
	rows, err := pool.Query(ctx, pgAggregateSQL)
	if err != nil {
		return aggResult{}, fmt.Errorf("pg aggregate query: %w", err)
	}
	defer rows.Close()

	groups := 0
	for rows.Next() {
		var country string
		var d time.Time
		var cnt int64
		var rev decimal.Decimal
		if err := rows.Scan(&country, &d, &cnt, &rev); err != nil {
			return aggResult{}, fmt.Errorf("pg aggregate scan: %w", err)
		}
		groups++
	}
	if rows.Err() != nil {
		return aggResult{}, fmt.Errorf("pg aggregate rows err: %w", rows.Err())
	}
	return aggResult{groups: groups, duration: time.Since(start)}, nil
}

// runMultiple прогоняет f n раз, возвращает срез длительностей ("несколько
// прогонов, характерный" — из требования брифа).
func runMultipleCH(ctx context.Context, conn clickhouse.Conn, n int) ([]aggResult, error) {
	out := make([]aggResult, 0, n)
	for i := 0; i < n; i++ {
		r, err := runCHAggregate(ctx, conn)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func runMultiplePG(ctx context.Context, pool *pgxpool.Pool, n int) ([]aggResult, error) {
	out := make([]aggResult, 0, n)
	for i := 0; i < n; i++ {
		r, err := runPGAggregate(ctx, pool)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func medianDuration(rs []aggResult) time.Duration {
	if len(rs) == 0 {
		return 0
	}
	ds := make([]time.Duration, len(rs))
	for i, r := range rs {
		ds[i] = r.duration
	}
	// insertion sort — n маленький (несколько прогонов)
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j-1] > ds[j]; j-- {
			ds[j-1], ds[j] = ds[j], ds[j-1]
		}
	}
	return ds[len(ds)/2]
}

func explainPG(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) "+pgAggregateSQL)
	if err != nil {
		return "", fmt.Errorf("pg explain: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("pg explain scan: %w", err)
		}
		lines = append(lines, line)
	}
	if rows.Err() != nil {
		return "", rows.Err()
	}
	return strings.Join(lines, "\n"), nil
}

func explainCH(ctx context.Context, conn clickhouse.Conn) (string, error) {
	rows, err := conn.Query(ctx, "EXPLAIN "+chAggregateSQL)
	if err != nil {
		return "", fmt.Errorf("ch explain: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("ch explain scan: %w", err)
		}
		lines = append(lines, line)
	}
	if rows.Err() != nil {
		return "", rows.Err()
	}
	return strings.Join(lines, "\n"), nil
}
