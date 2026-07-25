package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// mergeTreeDDL — DDL из Step 1 брифа: явный ORDER BY (event_time, user_id),
// PARTITION BY toYYYYMM(event_time), SETTINGS index_granularity=8192. Одна и
// та же схема (без суррогатного id — этот стенд не про точечный PK-поиск,
// это уже показано в when-olap; здесь — разреженный первичный индекс по
// ORDER BY и вставки) используется для всех таблиц демо, имя таблицы —
// параметр, чтобы держать основной bulk-датасет и вспомогательные
// анти-паттерн/async таблицы изолированными друг от друга.
const mergeTreeDDLTemplate = `
CREATE TABLE demo.%s
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

// createTable — DROP IF EXISTS + CREATE (идемпотентно для повторных прогонов).
func createTable(ctx context.Context, conn clickhouse.Conn, table string) error {
	if err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s", table)); err != nil {
		return fmt.Errorf("drop table %s: %w", table, err)
	}
	ddl := fmt.Sprintf(mergeTreeDDLTemplate, table)
	if err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create table %s: %w", table, err)
	}
	return nil
}

func dropTable(ctx context.Context, conn clickhouse.Conn, table string) error {
	return conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s", table))
}

// activeParts — число активных parts таблицы (system.parts, active=1).
func activeParts(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT count() FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}

// tableRows — число строк в таблице по system.parts (быстрее, чем count(*),
// не читает данные — согласуется с духом стенда: явные метаданные MergeTree).
func tableRows(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT coalesce(sum(rows), 0) FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}
