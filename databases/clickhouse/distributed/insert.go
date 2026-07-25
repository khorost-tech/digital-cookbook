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
// строк, тот же паттерн, что ../materialized-views/insert.go /
// ../go/mergetree/insert.go. ctx — уже несёт нужные per-query SETTINGS
// (напр. insert_distributed_sync=1 для синхронной вставки через Distributed,
// см. phaseInsertDist в shard.go) через clickhouse.Context(ctx,
// clickhouse.WithSettings(...)) на стороне вызывающего кода.
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

// syntheticMarkerRows — детерминированный набор из n строк с фиксированным
// маркером (eventType/country), НЕ пересекающимся с общим датасетом
// (../dataset/main.go: country коды из ISO alpha-2 реального списка,
// event_type из 8 реальных типов) — используется Step 3 брифа (репликация +
// дедупликация): байт-в-байт одинаковый набор строк, чтобы повторная
// отправка того же блока была настоящим дублем для ReplicatedMergeTree
// дедупликации, а не случайно другими данными.
func syntheticMarkerRows(n int, baseTime time.Time) []row {
	rows := make([]row, n)
	for i := 0; i < n; i++ {
		rows[i] = row{
			eventTime:  baseTime.Add(time.Duration(i) * time.Second),
			userID:     uint64(900_000_000 + i), // вне пула user_id общего датасета (0..500000)
			eventType:  "replication_probe",
			url:        "/replication-probe",
			durationMs: 1,
			country:    "ZZ", // маркер, отсутствует в dataset/main.go var countries
			revenue:    "0.00",
		}
	}
	return rows
}

// insertRows — вставка готового среза row (без CSV) в конкретное
// соединение/таблицу ОДНИМ батчем (PrepareBatch/Append/Send) — используется
// для marker-строк Step 3 (репликация напрямую в одну реплику, дедуп).
func insertRows(ctx context.Context, conn clickhouse.Conn, table string, rows []row) error {
	insertSQL := fmt.Sprintf("INSERT INTO demo.%s %s", table, insertColumns)
	batch, err := conn.PrepareBatch(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i, rw := range rows {
		if err := batch.Append(rw.eventTime, rw.userID, rw.eventType, rw.url, rw.durationMs, rw.country, rw.revenue); err != nil {
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}
