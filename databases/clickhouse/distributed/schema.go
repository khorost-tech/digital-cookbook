package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
)

const (
	tblEvents      = "events"             // ReplicatedMergeTree, локальная на каждой ноде (Step 2 брифа)
	tblDistributed = "events_distributed" // Distributed поверх events_cluster (Step 2 брифа)
	clusterName    = "events_cluster"     // имя кластера в ../config/remote-servers.xml
)

// replicatedDDLTemplate — ReplicatedMergeTree с путём в Keeper по шаблону
// брифа: '/clickhouse/tables/{shard}/events','{replica}'. {shard}/{replica}
// резолвятся из per-нода macros (../config/macros-*.xml) — ОДНА и та же
// DDL-строка выполняется на всех 4 нодах через ON CLUSTER, каждая нода
// подставляет СВОИ macros. Обе реплики шарда получают ОДИНАКОВЫЙ path (общий
// Keeper-координатор для этой пары), разный replica-идентификатор.
const replicatedDDLTemplate = `
CREATE TABLE demo.%s ON CLUSTER ` + clusterName + `
(
    event_time  DateTime,
    user_id     UInt64,
    event_type  LowCardinality(String),
    url         String,
    duration_ms UInt32,
    country     LowCardinality(String),
    revenue     Decimal(10,2)
)
ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/events', '{replica}')
ORDER BY (event_time, user_id)
PARTITION BY toYYYYMM(event_time)
SETTINGS index_granularity = 8192
`

// distributedDDLTemplate — Distributed-таблица поверх events_cluster,
// sharding key = cityHash64(user_id) (Step 2 брифа). ON CLUSTER создаёт её
// на всех 4 нодах — любая нода может быть точкой входа для INSERT/SELECT,
// Distributed сам фанаутит запрос по remote_servers-топологии.
const distributedDDLTemplate = `
CREATE TABLE demo.%s ON CLUSTER ` + clusterName + `
(
    event_time  DateTime,
    user_id     UInt64,
    event_type  LowCardinality(String),
    url         String,
    duration_ms UInt32,
    country     LowCardinality(String),
    revenue     Decimal(10,2)
)
ENGINE = Distributed(` + clusterName + `, demo, %s, cityHash64(user_id))
`

func execf(ctx context.Context, conn clickhouse.Conn, tmpl string, args ...any) error {
	return conn.Exec(ctx, fmt.Sprintf(tmpl, args...))
}

func dropTableOnCluster(ctx context.Context, conn clickhouse.Conn, name string) error {
	return conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS demo.%s ON CLUSTER %s", name, clusterName))
}

func createReplicatedTable(ctx context.Context, conn clickhouse.Conn, name string) error {
	if err := dropTableOnCluster(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	if err := execf(ctx, conn, replicatedDDLTemplate, name); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	return nil
}

func createDistributedTable(ctx context.Context, conn clickhouse.Conn, name, sourceTable string) error {
	if err := dropTableOnCluster(ctx, conn, name); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	if err := execf(ctx, conn, distributedDDLTemplate, name, sourceTable); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	return nil
}

// countLocal — count() на конкретной ноде-соединении по локальной (не
// Distributed) таблице — читает то, что физически лежит на диске ЭТОЙ ноды.
func countLocal(ctx context.Context, conn clickhouse.Conn, table string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s", table)).Scan(&n)
	return n, err
}

// countLocalWhere — count() локальной таблицы с произвольным WHERE (без
// параметров запроса — предложение подставляется вызывающим кодом, значения
// в этом стенде — фиксированные строковые маркеры, не пользовательский ввод).
func countLocalWhere(ctx context.Context, conn clickhouse.Conn, table, where string) (uint64, error) {
	var n uint64
	err := conn.QueryRow(ctx, fmt.Sprintf("SELECT count() FROM demo.%s WHERE %s", table, where)).Scan(&n)
	return n, err
}
