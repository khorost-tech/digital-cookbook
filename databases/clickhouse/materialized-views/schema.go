package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// Имена таблиц этого стенда — с префиксом mv_, чтобы не пересекаться с
// demo.events (when-olap, схема с суррогатным id) и demo.mergetree_events
// (стенд #2) — общая БД demo используется всеми стендами серии.
const (
	tblEvents     = "mv_events"           // сырая MergeTree (Step 1 брифа)
	tblEventsAgg  = "mv_events_agg"       // AggregatingMergeTree (Step 1)
	mvEventsToAgg = "mv_events_to_agg_mv" // MV: mv_events -> mv_events_agg

	tblSumming = "mv_events_summing" // SummingMergeTree (Step 2а)

	tblTTLDemo = "mv_events_ttl_demo" // TTL на партициях (Step 2в)

	tblKafkaQueue   = "mv_events_queue"        // ENGINE=Kafka (Step 3)
	mvKafkaToRaw    = "mv_events_queue_mv"     // MV: mv_events_queue -> mv_events_kafka_raw
	tblKafkaRaw     = "mv_events_kafka_raw"    // MergeTree, приёмник Kafka-потока
	tblKafkaAgg     = "mv_events_kafka_agg"    // AggregatingMergeTree, живой предагрегат
	mvKafkaRawToAgg = "mv_events_kafka_agg_mv" // MV: mv_events_kafka_raw -> mv_events_kafka_agg
)

// rawDDLTemplate — сырая таблица событий, тот же шаблон, что
// ../go/mergetree/schema.go (без суррогатного id — этот стенд не про
// точечный PK-поиск).
const rawDDLTemplate = `
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

// aggDDLTemplate — AggregatingMergeTree: одна строка на (country, час),
// -State-функции хранят промежуточное состояние агрегата (не готовое
// значение) — Step 1 брифа.
const aggDDLTemplate = `
CREATE TABLE demo.%s
(
    country       LowCardinality(String),
    hour          DateTime,
    revenue_state AggregateFunction(sum, Decimal(10,2)),
    users_state   AggregateFunction(uniq, UInt64),
    count_state   AggregateFunction(count)
)
ENGINE = AggregatingMergeTree
ORDER BY (country, hour)
`

// aggMVTemplate — MV как ТРИГГЕР: срабатывает на каждый INSERT в source-
// таблицу, прогоняет вставленный блок через GROUP BY + -State-функции и
// вставляет результат в целевую AggregatingMergeTree. НЕ обрабатывает
// существующие на момент CREATE строки source — только то, что вставляется
// ПОСЛЕ создания MV (content-note брифа, проверяется явно в agg.go).
const aggMVTemplate = `
CREATE MATERIALIZED VIEW demo.%s TO demo.%s AS
SELECT
    country,
    toStartOfHour(event_time) AS hour,
    sumState(revenue) AS revenue_state,
    uniqState(user_id) AS users_state,
    countState() AS count_state
FROM demo.%s
GROUP BY country, hour
`

const summingDDLTemplate = `
CREATE TABLE demo.%s
(
    country LowCardinality(String),
    hour    DateTime,
    revenue Decimal(18,2),
    events  UInt64
)
ENGINE = SummingMergeTree
ORDER BY (country, hour)
`

// ttlDDLTemplate — DDL с TTL DELETE на строках; PARTITION BY toDate — так
// партиция целиком становится кандидатом на быстрый дроп, когда все её
// строки просрочены (в отличие от построчного переписывания part).
// В проде — TTL event_time + INTERVAL 90 DAY (брифа Step 2), здесь для
// живой проверки в рамках одного хода — INTERVAL 3 DAY (ttl.go подставляет
// синтетические event_time в прошлом/настоящем, TTL сжат по времени, не по
// механизму).
const ttlDDLTemplate = `
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
TTL event_time + INTERVAL 3 DAY DELETE
`

// kafkaQueueDDLTemplate — ENGINE=Kafka: НЕ хранит данные на диске, это
// потоковый "вьюпорт" на топик. brokerList/topic/group — параметры (Step 3
// брифа). kafka_flush_interval_ms укорочен с дефолтных 7500ms, чтобы
// вставки из топика в MV становились видны быстрее в рамках демо-таймаута.
const kafkaQueueDDLTemplate = `
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
ENGINE = Kafka
SETTINGS
    kafka_broker_list = '%s',
    kafka_topic_list = '%s',
    kafka_group_name = '%s',
    kafka_format = 'JSONEachRow',
    kafka_num_consumers = 1,
    kafka_flush_interval_ms = 3000,
    kafka_skip_broken_messages = 0
`

// kafkaMVTemplate — MV-триггер: ENGINE=Kafka таблица сама по себе не
// накапливает данные, читает их только через прикреплённый MV (фоновый
// поток консюмера запускается, когда к Kafka-таблице привязан хотя бы один
// MV с SELECT из неё).
const kafkaMVTemplate = `
CREATE MATERIALIZED VIEW demo.%s TO demo.%s AS
SELECT event_time, user_id, event_type, url, duration_ms, country, revenue
FROM demo.%s
`

func execf(ctx context.Context, conn clickhouse.Conn, tmpl string, args ...any) error {
	return conn.Exec(ctx, fmt.Sprintf(tmpl, args...))
}

func dropTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	return conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s", name))
}

func createRawTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	if err := execf(ctx, conn, rawDDLTemplate, name); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	return nil
}

func createAggTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	if err := execf(ctx, conn, aggDDLTemplate, name); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	return nil
}

func createAggMV(ctx context.Context, conn clickhouse.Conn, mvName, targetTable, sourceTable string) error {
	if err := dropTable(ctx, conn, mvName); err != nil {
		return fmt.Errorf("drop %s: %w", mvName, err)
	}
	if err := execf(ctx, conn, aggMVTemplate, mvName, targetTable, sourceTable); err != nil {
		return fmt.Errorf("create %s: %w", mvName, err)
	}
	return nil
}

func createSummingTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return execf(ctx, conn, summingDDLTemplate, name)
}

func createTTLTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return execf(ctx, conn, ttlDDLTemplate, name)
}

func createKafkaQueueTable(ctx context.Context, conn clickhouse.Conn, name, brokerList, topic, group string) error {
	if err := dropTable(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	return execf(ctx, conn, kafkaQueueDDLTemplate, name, brokerList, topic, group)
}

func createKafkaMV(ctx context.Context, conn clickhouse.Conn, mvName, targetTable, sourceTable string) error {
	if err := dropTable(ctx, conn, mvName); err != nil {
		return fmt.Errorf("drop %s: %w", mvName, err)
	}
	return execf(ctx, conn, kafkaMVTemplate, mvName, targetTable, sourceTable)
}

// activeParts — число активных parts таблицы (system.parts, active=1).
func activeParts(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT count() FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}

// tableRows — число строк таблицы по system.parts (не читает данные).
func tableRows(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, "SELECT coalesce(sum(rows), 0) FROM system.parts WHERE database = 'demo' AND table = ? AND active", table).Scan(&n)
	return n, err
}

// countRows — SELECT count() напрямую (для Kafka-таблиц, где мы опрашиваем
// небольшой объём и хотим авторитетный, а не метаданный, счётчик).
func countRows(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s", table)).Scan(&n)
	return n, err
}
