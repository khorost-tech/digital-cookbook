package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// opsDDLTemplate — тот же общий шаблон датасета (event_time/user_id/...),
// что ../go/mergetree/schema.go, но с параметризуемыми CODEC на event_time
// и index_granularity — нужны для сравнения кодеков и разной гранулярности
// (Step 4 брифа). codec="" — дефолтный кодек сервера (LZ4, без явной
// компрессионной политики в compose/compose.yml).
//
// min_bytes_for_wide_part=0 — живая деталь, найденная во время прогона:
// таблицы этого стенда небольшие (десятки-сотни тысяч строк), их parts
// остаются в дефолтном формате Compact (< 10MiB на part), а в Compact-parts
// ClickHouse хранит ВСЕ колонки в одном физическом файле — system.columns/
// system.parts_columns.data_compressed_bytes для КАЖДОЙ колонки в этом
// случае возвращает размер ВСЕГО part'а, а не колонки (проверено живьём:
// без этой настройки все 7 колонок ops_events показывали идентичные байты).
// min_bytes_for_wide_part=0 форсирует Wide-формат (файл на колонку) с
// первого же part'а — обязательное условие для честного Step 3/Step 4
// сравнения размера ПО КОЛОНКАМ.
const opsDDLTemplate = `
CREATE TABLE demo.%s
(
    event_time  DateTime%s,
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
SETTINGS index_granularity = %d, min_bytes_for_wide_part = 0
`

// createTable — DROP IF EXISTS + CREATE (идемпотентно для повторных
// прогонов). codec — суффикс " CODEC(...)" для event_time, пустая строка —
// без явного CODEC (дефолт сервера).
func createTable(ctx context.Context, conn clickhouse.Conn, table, codec string, granularity int) error {
	if err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s", table)); err != nil {
		return fmt.Errorf("drop table %s: %w", table, err)
	}
	codecSuffix := ""
	if codec != "" {
		codecSuffix = fmt.Sprintf(" CODEC(%s)", codec)
	}
	ddl := fmt.Sprintf(opsDDLTemplate, table, codecSuffix, granularity)
	if err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create table %s (ddl=%s): %w", table, ddl, err)
	}
	return nil
}

// activeParts — число активных parts таблицы (system.parts, active=1).
func activeParts(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT count() FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}

// tableRows — число строк в таблице по system.parts (не читает данные).
func tableRows(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT coalesce(sum(rows), 0) FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}

// mergesInProgress — число мутаций/слияний в system.merges прямо сейчас
// (окно наблюдения фонового merge, Step 1 брифа).
func mergesInProgress(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT count() FROM system.merges WHERE database = 'demo' AND table = ?", table).Scan(&n)
	return n, err
}

// columnBytes — сжатый/несжатый размер колонки, сумма по всем активным
// parts таблицы. sum(UInt64) в ClickHouse возвращает UInt64 (не Int64) —
// сканируем в uint64 и приводим к int64 для humanBytes.
//
// Живая деталь: system.columns.data_compressed_bytes/data_uncompressed_bytes
// (метрика брифа дословно) на прогоне этого стенда оказалась 0 сразу после
// RESTORE TABLE (metadata-кеш system.columns не обновился синхронно с
// восстановлением) — при этом system.parts_columns.* для тех же parts уже
// показывал реальные ненулевые байты. system.parts_columns — источник
// правды по факту parts на диске (агрегирует напрямую по активным parts, не
// кешируемое представление таблицы), используем его вместо system.columns.
func columnBytes(ctx context.Context, conn clickhouse.Conn, table, column string) (compressed, uncompressed int64, err error) {
	var c, u uint64
	err = conn.QueryRow(ctx, `
		SELECT coalesce(sum(data_compressed_bytes), 0), coalesce(sum(data_uncompressed_bytes), 0)
		FROM system.parts_columns
		WHERE database = 'demo' AND table = ? AND column = ? AND active`, table, column).Scan(&c, &u)
	return int64(c), int64(u), err
}
