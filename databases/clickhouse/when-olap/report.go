package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

type sizeReport struct {
	chBytes uint64
	chParts uint64
	chRows  uint64
	pgBytes int64
	pgTable int64
	pgIdx   int64
	pgRows  int64
}

func measureSize(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool) (sizeReport, error) {
	var sr sizeReport
	if err := ch.QueryRow(ctx, `
		SELECT sum(bytes_on_disk), count(), sum(rows)
		FROM system.parts
		WHERE database = 'demo' AND table = 'events' AND active`).Scan(&sr.chBytes, &sr.chParts, &sr.chRows); err != nil {
		return sr, fmt.Errorf("ch size: %w", err)
	}

	if err := pg.QueryRow(ctx, "SELECT pg_total_relation_size('events'), pg_relation_size('events'), pg_indexes_size('events')").
		Scan(&sr.pgBytes, &sr.pgTable, &sr.pgIdx); err != nil {
		return sr, fmt.Errorf("pg size: %w", err)
	}
	if err := pg.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&sr.pgRows); err != nil {
		return sr, fmt.Errorf("pg count: %w", err)
	}
	return sr, nil
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

// assertFailFast — fail-loud: печатает провал и завершает процесс ненулевым
// кодом. Используется для всех программных ассертов сценария (см. брифа
// Step 4: "программные ассерты (fail-loud)").
func assertFailFast(cond bool, format string, args ...any) {
	if !cond {
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("[ASSERT FAILED] %s\n", msg)
		panic("assertion failed: " + msg)
	}
	fmt.Printf("[assert OK] %s\n", fmt.Sprintf(format, args...))
}
