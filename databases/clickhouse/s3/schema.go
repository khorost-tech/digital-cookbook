package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// Имена таблиц этого стенда — с префиксом s3_, чтобы не пересекаться с
// таблицами других стендов серии в общей БД demo.
const (
	tblEvents      = "s3_events"             // тиринг-таблица (Step 1/2 брифа), storage_policy=hot_cold
	tblFromParquet = "s3_events_from_parquet" // приёмник ingestion из Parquet/S3 (Step 3 брифа)
)

// eventsDDLTemplate — та же схема датасета, что весь остальной cookbook
// серии (event_time/user_id/event_type/url/duration_ms/country/revenue),
// НО PARTITION BY toDate (не toYYYYMM) — тот же приём, что
// ../materialized-views/ttl.go: партиция целиком уезжает, а не
// переписывается построчно. storage_policy=hot_cold — Step 1 брифа
// (../config/storage.xml): том "hot" (default, локальный диск) + том
// "cold" (s3, MinIO). TTL ... TO VOLUME 'cold' документирует ПРОД-
// механизм автоматического переноса; живой прогон форсирует перенос
// явным ALTER TABLE ... MOVE PARTITION ... TO DISK 's3' (см. tiering.go)
// для детерминизма в рамках одного хода — тот же честный выбор, что
// "OPTIMIZE ... FINAL" в TTL-демо стенда #4 (TTL "ленив", фон может
// подобрать сам, демо не полагается на угаданный момент фона).
const eventsDDLTemplate = `
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
PARTITION BY toDate(event_time)
TTL event_time + INTERVAL 30 DAY TO VOLUME 'cold'
SETTINGS storage_policy = 'hot_cold', index_granularity = 8192
`

// fromParquetDDLTemplate — приёмник ingestion из S3/Parquet (Step 3
// брифа), обычный default storage_policy — это демонстрация "S3 ->
// MergeTree", не про тиринг конкретно этой таблицы.
const fromParquetDDLTemplate = `
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
PARTITION BY toDate(event_time)
`

func execf(ctx context.Context, conn clickhouse.Conn, tmpl string, args ...any) error {
	return conn.Exec(ctx, fmt.Sprintf(tmpl, args...))
}

func dropTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	return conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s", name))
}

func createEventsTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return execf(ctx, conn, eventsDDLTemplate, name)
}

func createFromParquetTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return execf(ctx, conn, fromParquetDDLTemplate, name)
}

// partInfo — одна активная часть таблицы: партиция, диск, число строк,
// размер на диске (system.parts). disk_name — центральный факт Step 2
// брифа (подтверждает физическое размещение части после MOVE PARTITION).
type partInfo struct {
	partition string
	diskName  string
	rows      uint64
	bytes     int64
}

func listParts(ctx context.Context, conn clickhouse.Conn, table string) ([]partInfo, error) {
	rows, err := conn.Query(ctx, `
		SELECT partition, disk_name, rows, bytes_on_disk
		FROM system.parts
		WHERE database = 'demo' AND table = ? AND active
		ORDER BY partition, name`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []partInfo
	for rows.Next() {
		var p partInfo
		// bytes_on_disk в system.parts — UInt64 (не Int64) в этой версии CH
		// (26.6.1.1193) — живая находка, тот же класс ошибки маппинга типов,
		// что when-olap/pointops.go нашёл на system.mutations.parts_to_do
		// (там наоборот: Int64, а не UInt64).
		var bytesOnDisk uint64
		if err := rows.Scan(&p.partition, &p.diskName, &p.rows, &bytesOnDisk); err != nil {
			return nil, err
		}
		p.bytes = int64(bytesOnDisk)
		out = append(out, p)
	}
	return out, rows.Err()
}

func countRows(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s", table)).Scan(&n)
	return n, err
}
