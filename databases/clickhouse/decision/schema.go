package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chDDL — тот же шаблон MergeTree, что и стенд #1 (when-olap): ORDER BY
// (event_time, user_id), PARTITION BY toYYYYMM(event_time). Без суррогатного
// id — этот стенд не про точечные операции, только про один аналитический
// агрегат на общем датасете.
const chDDL = `
CREATE TABLE demo.decision_events
(
    event_time  DateTime,
    user_id     UInt64,
    event_type  LowCardinality(String),
    url         String,
    duration_ms UInt32,
    country     LowCardinality(String),
    revenue     Decimal(10,2)
)
ENGINE = MergeTree
ORDER BY (event_time, user_id)
PARTITION BY toYYYYMM(event_time)
SETTINGS index_granularity = 8192
`

// pgLikeDDL — общая схема ОБЫЧНОЙ таблицы: используется и как PG-baseline
// (без индексов — стенд #1 когда-olap уже показал, что btree на
// event_time/country не помогают этому агрегату), и как исходная таблица
// TimescaleDB ДО create_hypertable (сама гипертаблица — та же DDL, только
// после неё вызывается create_hypertable()).
const pgLikeDDL = `
CREATE TABLE decision_events (
    event_time  TIMESTAMP NOT NULL,
    user_id     BIGINT NOT NULL,
    event_type  TEXT NOT NULL,
    url         TEXT NOT NULL,
    duration_ms INT NOT NULL,
    country     TEXT NOT NULL,
    revenue     NUMERIC(10,2) NOT NULL
)
`

func createSchemaCH(ctx context.Context, ch clickhouse.Conn) error {
	if err := ch.Exec(ctx, "DROP TABLE IF EXISTS demo.decision_events"); err != nil {
		return fmt.Errorf("ch drop table: %w", err)
	}
	if err := ch.Exec(ctx, chDDL); err != nil {
		return fmt.Errorf("ch create table: %w", err)
	}
	fmt.Println("[schema] CH demo.decision_events (MergeTree ORDER BY (event_time, user_id) PARTITION BY toYYYYMM(event_time)) — created")
	return nil
}

func createSchemaPG(ctx context.Context, pg *pgxpool.Pool) error {
	if _, err := pg.Exec(ctx, "DROP TABLE IF EXISTS decision_events"); err != nil {
		return fmt.Errorf("pg drop table: %w", err)
	}
	if _, err := pg.Exec(ctx, pgLikeDDL); err != nil {
		return fmt.Errorf("pg create table: %w", err)
	}
	fmt.Println("[schema] PG decision_events (обычная таблица, БЕЗ индексов — baseline) — created")
	return nil
}

func createSchemaTimescale(ctx context.Context, ts *pgxpool.Pool) error {
	if _, err := ts.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		return fmt.Errorf("ts create extension: %w", err)
	}
	// DROP CASCADE — на повторных прогонах continuous aggregate (decision_events_daily,
	// создаётся позже фазой timescale) зависит от гипертаблицы; чистый повторный
	// прогон с нуля не должен падать на "cannot drop table because other objects depend on it".
	if _, err := ts.Exec(ctx, "DROP TABLE IF EXISTS decision_events CASCADE"); err != nil {
		return fmt.Errorf("ts drop table: %w", err)
	}
	if _, err := ts.Exec(ctx, pgLikeDDL); err != nil {
		return fmt.Errorf("ts create table: %w", err)
	}
	if _, err := ts.Exec(ctx, "SELECT create_hypertable('decision_events', 'event_time')"); err != nil {
		return fmt.Errorf("ts create_hypertable: %w", err)
	}
	fmt.Println("[schema] Timescale decision_events (create_hypertable по event_time, дефолтный chunk_time_interval=7 days) — created")
	return nil
}

func createSchema(ctx context.Context, ch clickhouse.Conn, pg, ts *pgxpool.Pool) error {
	if err := createSchemaCH(ctx, ch); err != nil {
		return err
	}
	if err := createSchemaPG(ctx, pg); err != nil {
		return err
	}
	if err := createSchemaTimescale(ctx, ts); err != nil {
		return err
	}
	return nil
}
