package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tsCaggDDL — continuous aggregate: материализованное представление,
// авто-обновляемое (Step 2 брифа). WITH NO DATA — сразу пустое, наполняется
// явным CALL refresh_continuous_aggregate(...) ниже (в проде это делает
// add_continuous_aggregate_policy() по расписанию — здесь держим обзорно:
// один ручной refresh демонстрирует механизм без ожидания scheduler'а).
const tsCaggDDL = `
CREATE MATERIALIZED VIEW decision_events_daily
WITH (timescaledb.continuous) AS
SELECT
    country,
    time_bucket('1 day', event_time) AS day,
    count(*) AS cnt,
    sum(revenue) AS rev
FROM decision_events
GROUP BY country, day
WITH NO DATA
`

func createContinuousAggregate(ctx context.Context, ts *pgxpool.Pool) error {
	if _, err := ts.Exec(ctx, "DROP MATERIALIZED VIEW IF EXISTS decision_events_daily"); err != nil {
		return fmt.Errorf("ts drop cagg: %w", err)
	}
	if _, err := ts.Exec(ctx, tsCaggDDL); err != nil {
		return fmt.Errorf("ts create cagg: %w", err)
	}
	fmt.Println("[timescale] decision_events_daily (CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous), WITH NO DATA) — created")
	return nil
}

// refreshContinuousAggregate — CALL refresh_continuous_aggregate(view, NULL, NULL)
// материализует ВЕСЬ диапазон сразу (NULL/NULL = без границ). В проде это
// делает фоновая политика add_continuous_aggregate_policy(schedule_interval
// => ...) — "авто-обновление" из брифа; здесь вызывается вручную, ОДИН раз,
// чтобы не зависеть от таймера background job'а внутри одного прогона стенда
// (честная оговорка — не выдаётся за работающую политику).
func refreshContinuousAggregate(ctx context.Context, ts *pgxpool.Pool) (time.Duration, error) {
	start := time.Now()
	if _, err := ts.Exec(ctx, "CALL refresh_continuous_aggregate('decision_events_daily', NULL, NULL)"); err != nil {
		return 0, fmt.Errorf("ts refresh cagg: %w", err)
	}
	return time.Since(start), nil
}

func caggRowCount(ctx context.Context, ts *pgxpool.Pool) (int64, error) {
	var n int64
	if err := ts.QueryRow(ctx, "SELECT count(*) FROM decision_events_daily").Scan(&n); err != nil {
		return 0, fmt.Errorf("ts cagg count: %w", err)
	}
	return n, nil
}

// enableCompression + compressAllChunks — native compression (Step 2
// брифа). compress_segmentby=country группирует строки одного сегмента
// вместе внутри compressed chunk (обычный совет для колонок низкой
// кардинальности, участвующих в GROUP BY/WHERE — тот же принцип, что
// LowCardinality в ClickHouse), compress_orderby=event_time — колонка
// времени почти монотонна внутри чанка (тот же эффект, что Delta-кодек CH
// стенда #6).
func enableCompression(ctx context.Context, ts *pgxpool.Pool) error {
	_, err := ts.Exec(ctx, `
ALTER TABLE decision_events SET (
    timescaledb.compress,
    timescaledb.compress_orderby = 'event_time',
    timescaledb.compress_segmentby = 'country'
)`)
	if err != nil {
		return fmt.Errorf("ts enable compression: %w", err)
	}
	return nil
}

func compressAllChunks(ctx context.Context, ts *pgxpool.Pool) (chunks int, elapsed time.Duration, err error) {
	start := time.Now()
	rows, err := ts.Query(ctx, "SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('decision_events') c")
	if err != nil {
		return 0, 0, fmt.Errorf("ts compress_chunk: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var compressed any
		if err := rows.Scan(&compressed); err != nil {
			return chunks, time.Since(start), fmt.Errorf("ts compress_chunk scan: %w", err)
		}
		chunks++
	}
	if rows.Err() != nil {
		return chunks, time.Since(start), rows.Err()
	}
	return chunks, time.Since(start), nil
}

func chunkCount(ctx context.Context, ts *pgxpool.Pool) (int, error) {
	rows, err := ts.Query(ctx, "SELECT * FROM show_chunks('decision_events')")
	if err != nil {
		return 0, fmt.Errorf("show_chunks: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}
