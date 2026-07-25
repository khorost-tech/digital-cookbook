package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const insertColumns = "(event_time, user_id, event_type, url, duration_ms, country, revenue)"

// batchLoad читает limit строк (0 = до конца файла) из CSV, пропустив первые
// skip строк, вставляет чанками по batchSize через PrepareBatch/Append/Send.
// skip позволяет нескольким таблицам этого стенда делить один общий CSV на
// непересекающиеся диапазоны (Step 1 и Step 4 брифа читают разные окна
// одного файла — ../dataset/main.go -rows=750000).
func batchLoad(ctx context.Context, conn clickhouse.Conn, table, csvPath string, skip, batchSize int, limit int64) (rows uint64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()
	for i := 0; i < skip; i++ {
		if _, ok := cr.next(); !ok {
			return 0, 0, fmt.Errorf("csv %s has fewer than skip=%d rows (err=%v)", csvPath, skip, cr.err)
		}
	}

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

// loadManySmallBatches — Step 1 брифа: batches отдельных PrepareBatch/Send
// вызовов по batchSize строк каждый (одна открытая CSV-последовательность на
// весь вызов, БЕЗ повторного skip — в отличие от batchLoad с растущим skip,
// это O(n), не O(n^2)). Каждый Send — отдельный part (при SYSTEM STOP
// MERGES) — это и есть демонстрация "много мелких вставок -> много parts".
func loadManySmallBatches(ctx context.Context, conn clickhouse.Conn, table, csvPath string, batches, batchSize int) (rows uint64, elapsed time.Duration, err error) {
	cr, err := openCSVRowReader(csvPath)
	if err != nil {
		return 0, 0, err
	}
	defer cr.close()

	insertSQL := fmt.Sprintf("INSERT INTO demo.%s %s", table, insertColumns)
	start := time.Now()
	for i := 0; i < batches; i++ {
		b, err := conn.PrepareBatch(ctx, insertSQL)
		if err != nil {
			return rows, time.Since(start), fmt.Errorf("prepare batch %d/%d: %w", i+1, batches, err)
		}
		n := 0
		for n < batchSize {
			rw, ok := cr.next()
			if !ok {
				break
			}
			if err := b.Append(rw.eventTime, rw.userID, rw.eventType, rw.url, rw.durationMs, rw.country, rw.revenue); err != nil {
				return rows, time.Since(start), fmt.Errorf("append row in batch %d/%d: %w", i+1, batches, err)
			}
			n++
		}
		if cr.err != nil {
			return rows, time.Since(start), cr.err
		}
		if n == 0 {
			break
		}
		if err := b.Send(); err != nil {
			return rows, time.Since(start), fmt.Errorf("send batch %d/%d: %w", i+1, batches, err)
		}
		rows += uint64(n)
	}
	return rows, time.Since(start), nil
}
