package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// nativeInsert — clickhouse-go/v2 native batch API: PrepareBatch/Append/Send
// (тот же приём, что ../../go/mergetree/insert.go), batchSize строк на
// вызов Send — apples-to-apples с ch-go/database-sql батчами этого стенда.
func nativeInsert(ctx context.Context, conn clickhouse.Conn, table, csvPath string, batchSize int) (rows uint64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	insertSQL := fmt.Sprintf("INSERT INTO demo.%s (event_time, user_id, event_type, url, duration_ms, country, revenue)", table)

	start := time.Now()
	var batch driver.Batch
	inBatch := 0
	newBatch := func() error {
		b, err := conn.PrepareBatch(ctx, insertSQL)
		if err != nil {
			return err
		}
		batch = b
		inBatch = 0
		return nil
	}
	if err := newBatch(); err != nil {
		return 0, 0, fmt.Errorf("prepare batch: %w", err)
	}

	for {
		rw, ok := cr.next()
		if !ok {
			break
		}
		if err := batch.Append(rw.eventTime, rw.userID, rw.eventType, rw.url, rw.durationMs, rw.country, rw.revenue); err != nil {
			return rows, time.Since(start), fmt.Errorf("batch append row %d: %w", rows+1, err)
		}
		inBatch++
		rows++
		if inBatch >= batchSize {
			if err := batch.Send(); err != nil {
				return rows, time.Since(start), fmt.Errorf("batch send at row %d: %w", rows, err)
			}
			if err := newBatch(); err != nil {
				return rows, time.Since(start), fmt.Errorf("prepare next batch: %w", err)
			}
		}
	}
	if cr.err != nil {
		return rows, time.Since(start), cr.err
	}
	if inBatch > 0 {
		if err := batch.Send(); err != nil {
			return rows, time.Since(start), fmt.Errorf("final batch send: %w", err)
		}
	}
	return rows, time.Since(start), nil
}

// nativeSelect — тот же аналитический SELECT (aggregate.go) через
// clickhouse-go/v2 native Query/Scan (высокоуровневый маппинг типов — в
// отличие от ch-go, тут country/c/cents просто Scan()-ятся в string/uint64/
// int64 без ручной работы с proto.Col*).
func nativeSelect(ctx context.Context, conn clickhouse.Conn, table string) (aggResult, time.Duration, error) {
	start := time.Now()
	rows, err := conn.Query(ctx, fmt.Sprintf(aggregateSQLTemplate, table))
	if err != nil {
		return aggResult{}, time.Since(start), fmt.Errorf("native select: %w", err)
	}
	defer rows.Close()

	var triples []triple
	for rows.Next() {
		var t triple
		if err := rows.Scan(&t.country, &t.c, &t.cents); err != nil {
			return aggResult{}, time.Since(start), fmt.Errorf("native select scan: %w", err)
		}
		triples = append(triples, t)
	}
	dur := time.Since(start)
	if err := rows.Err(); err != nil {
		return aggResult{}, dur, fmt.Errorf("native select rows: %w", err)
	}
	return computeAgg(triples), dur, nil
}
