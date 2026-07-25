package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const insertColumns = "(event_time, user_id, event_type, url, duration_ms, country, revenue)"

// batchLoad — вставка через PrepareBatch/Append/Send чанками по batchSize
// строк. limit>0 ограничивает число строк, прочитанных из CSV (0 — весь
// файл). Тот же паттерн, что ../go/mergetree/insert.go.
func batchLoad(ctx context.Context, conn clickhouse.Conn, table, csvPath string, batchSize int, limit int64) (rows uint64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	insertSQL := fmt.Sprintf("INSERT INTO demo.%s %s", table, insertColumns)

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
		if limit > 0 && int64(rows) >= limit {
			break
		}
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
