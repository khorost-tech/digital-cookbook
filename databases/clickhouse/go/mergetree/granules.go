package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/shopspring/decimal"
)

// granuleFilterSQL/granuleFullSQL — один и тот же агрегат по таблице bulk-
// датасета, один раз с фильтром по префиксу ORDER BY (event_time) — узкое
// окно в 1 сутки внутри 90-дневного окна датасета, — и один раз без фильтра
// (полный скан, для контраста read_rows). Step 1 брифа: "запрос с фильтром
// по префиксу ORDER BY -> пропуск гранул".
const granuleFilterSQL = `
SELECT count(), sum(revenue)
FROM demo.mergetree_events
WHERE event_time >= '2026-06-15 00:00:00' AND event_time < '2026-06-16 00:00:00'
`

const granuleFullSQL = `
SELECT count(), sum(revenue)
FROM demo.mergetree_events
`

// explainIndexes — EXPLAIN indexes=1 текст плана (Parts/Granules read/total
// из секции Indexes — детерминированное число, не host-зависимое).
func explainIndexes(ctx context.Context, conn clickhouse.Conn, sql string) (string, error) {
	rows, err := conn.Query(ctx, "EXPLAIN indexes = 1 "+sql)
	if err != nil {
		return "", fmt.Errorf("explain indexes: %w", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("explain indexes scan: %w", err)
		}
		lines = append(lines, line)
	}
	if rows.Err() != nil {
		return "", rows.Err()
	}
	return strings.Join(lines, "\n"), nil
}

type queryStat struct {
	duration time.Duration
	count    uint64
	readRows uint64
}

// runWithReadRows выполняет sql с явным query_id, затем SYSTEM FLUSH LOGS +
// читает read_rows из system.query_log (реальное число прочитанных строк —
// в отличие от result count(), это то, сколько строк ClickHouse физически
// прочитал с диска, до/после пропуска гранул по первичному индексу).
func runWithReadRows(ctx context.Context, conn clickhouse.Conn, sql, queryIDPrefix string) (queryStat, error) {
	queryID := fmt.Sprintf("%s-%d", queryIDPrefix, time.Now().UnixNano())
	qctx := clickhouse.Context(ctx, clickhouse.WithQueryID(queryID))

	start := time.Now()
	var cnt uint64
	var rev decimal.Decimal
	if err := conn.QueryRow(qctx, sql).Scan(&cnt, &rev); err != nil {
		return queryStat{}, fmt.Errorf("run query: %w", err)
	}
	dur := time.Since(start)

	if err := conn.Exec(ctx, "SYSTEM FLUSH LOGS"); err != nil {
		return queryStat{}, fmt.Errorf("flush logs: %w", err)
	}
	var readRows uint64
	err := conn.QueryRow(ctx, `
		SELECT read_rows FROM system.query_log
		WHERE query_id = ? AND type = 'QueryFinish'
		ORDER BY event_time DESC LIMIT 1`, queryID).Scan(&readRows)
	if err != nil {
		return queryStat{duration: dur, count: cnt}, fmt.Errorf("read read_rows from query_log: %w", err)
	}
	return queryStat{duration: dur, count: cnt, readRows: readRows}, nil
}
