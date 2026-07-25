package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sizeCH(ctx context.Context, ch clickhouse.Conn) (bytesOnDisk uint64, parts uint64, rows uint64, err error) {
	row := ch.QueryRow(ctx, `
		SELECT sum(bytes_on_disk), count(), sum(rows)
		FROM system.parts
		WHERE database = 'demo' AND table = 'decision_events' AND active`)
	if err := row.Scan(&bytesOnDisk, &parts, &rows); err != nil {
		return 0, 0, 0, fmt.Errorf("ch size: %w", err)
	}
	return bytesOnDisk, parts, rows, nil
}

func sizePGLike(ctx context.Context, pool *pgxpool.Pool) (bytesTotal int64, rows int64, err error) {
	if err := pool.QueryRow(ctx, "SELECT pg_total_relation_size('decision_events')").Scan(&bytesTotal); err != nil {
		return 0, 0, fmt.Errorf("pg_total_relation_size: %w", err)
	}
	rows, err = countDecisionEvents(ctx, pool)
	if err != nil {
		return bytesTotal, 0, err
	}
	return bytesTotal, rows, nil
}

func countDecisionEvents(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	var rows int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM decision_events").Scan(&rows); err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return rows, nil
}

// sizeHypertable — hypertable_size() агрегирует размер ВСЕХ чанков
// гипертаблицы (включая индексы) — TimescaleDB-аналог pg_total_relation_size
// для обычной таблицы. Вызывается и ДО, и ПОСЛЕ compress_chunk (Step 2
// брифа: "эффект на размер").
func sizeHypertable(ctx context.Context, pool *pgxpool.Pool) (bytesTotal int64, err error) {
	if err := pool.QueryRow(ctx, "SELECT hypertable_size('decision_events')").Scan(&bytesTotal); err != nil {
		return 0, fmt.Errorf("hypertable_size: %w", err)
	}
	return bytesTotal, nil
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// assertFailFast — fail-loud: печатает провал и завершает процесс паникой.
// Тот же приём, что стенды #1-#7 серии (см. когда-olap/report.go).
func assertFailFast(cond bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !cond {
		fmt.Printf("[ASSERT FAILED] %s\n", msg)
		panic("assertion failed: " + msg)
	}
	fmt.Printf("[assert OK] %s\n", msg)
}
