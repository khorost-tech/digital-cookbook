package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// ddlTemplate — та же схема, что во всех предыдущих стендах серии (общий
// датасет ../../dataset/main.go, DDL ../../go/mergetree/schema.go). Один
// шаблон, имя таблицы — параметр: каждый драйвер вставляет одинаковый CSV в
// СВОЮ таблицу (apples-to-apples throughput/latency, без конкуренции за один
// и тот же parts-набор), а верификационный SELECT (aggregate.go) сравнивает
// результат по всем таблицам.
const ddlTemplate = `
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

// createTableAdmin/tableRowsAdmin работают через ОДНО административное
// clickhouse-go/v2 native-соединение (main.go), общее для всех фаз —
// создание/подсчёт строк таблицы не является частью замера конкретного
// драйвера (это было бы нечестно — толщина стенда, а не throughput клиента).
func createTableAdmin(ctx context.Context, conn clickhouse.Conn, table string) error {
	if err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s", table)); err != nil {
		return fmt.Errorf("drop table %s: %w", table, err)
	}
	if err := conn.Exec(ctx, fmt.Sprintf(ddlTemplate, table)); err != nil {
		return fmt.Errorf("create table %s: %w", table, err)
	}
	return nil
}

// tableRowsAdmin — число строк таблицы по system.parts (быстрее, чем
// count(*), не читает данные — тот же приём, что в mergetree/schema.go).
func tableRowsAdmin(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT coalesce(sum(rows), 0) FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}
