package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// chDDL — MergeTree с явными ORDER BY/PARTITION BY из брифа серии (те же,
// что будут использоваться стендом #2 "MergeTree Go/Java"). id — суррогатный
// PK для честной демонстрации точечных операций (см. README "где CH
// противопоказан"): в исходной схеме датасета его нет, добавлен здесь как
// обычная инженерная практика (в реальных OLTP-таблицах PK почти всегда есть).
const chDDL = `
CREATE TABLE demo.events
(
    id          UInt64,
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

// pgDDL — таблица + btree-индексы на event_time/country (из брифа), PRIMARY
// KEY(id) для точечных операций (та же роль, что ORDER BY в CH, но btree
// точечного поиска, а не разреженный первичный индекс диапазонов).
const pgDDL = `
CREATE TABLE events (
    id          BIGINT PRIMARY KEY,
    event_time  TIMESTAMP NOT NULL,
    user_id     BIGINT NOT NULL,
    event_type  TEXT NOT NULL,
    url         TEXT NOT NULL,
    duration_ms INT NOT NULL,
    country     TEXT NOT NULL,
    revenue     NUMERIC(10,2) NOT NULL
)
`

const pgIdxEventTime = `CREATE INDEX idx_events_event_time ON events (event_time)`
const pgIdxCountry = `CREATE INDEX idx_events_country ON events (country)`

func createSchema(ctx context.Context, ch clickhouse.Conn, pg *pgxpool.Pool) error {
	if err := ch.Exec(ctx, "DROP TABLE IF EXISTS demo.events"); err != nil {
		return fmt.Errorf("ch drop table: %w", err)
	}
	if err := ch.Exec(ctx, chDDL); err != nil {
		return fmt.Errorf("ch create table: %w", err)
	}

	if _, err := pg.Exec(ctx, "DROP TABLE IF EXISTS events"); err != nil {
		return fmt.Errorf("pg drop table: %w", err)
	}
	if _, err := pg.Exec(ctx, pgDDL); err != nil {
		return fmt.Errorf("pg create table: %w", err)
	}
	if _, err := pg.Exec(ctx, pgIdxEventTime); err != nil {
		return fmt.Errorf("pg create index event_time: %w", err)
	}
	if _, err := pg.Exec(ctx, pgIdxCountry); err != nil {
		return fmt.Errorf("pg create index country: %w", err)
	}

	fmt.Println("[schema] CH demo.events (MergeTree ORDER BY (event_time, user_id) PARTITION BY toYYYYMM(event_time)) — created")
	fmt.Println("[schema] PG events + idx_events_event_time + idx_events_country (btree) — created")
	return nil
}
