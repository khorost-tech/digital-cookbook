package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// clusterRow — одна строка system.clusters для events_cluster.
type clusterRow struct {
	shardNum   uint32
	replicaNum uint32
	hostName   string
}

// phaseCluster — Step 1 брифа: проверка топологии кластера ДО создания
// таблиц. system.clusters — статическая проекция remote_servers.xml
// (../config/remote-servers.xml), не зависит от того, есть ли уже
// какие-либо таблицы. system.zookeeper WHERE path='/' — проверка, что CH
// вообще может достучаться до Keeper (keeper1, compose/cluster.yml) —
// возвращает служебные znode'ы Keeper (напр. 'keeper', 'clickhouse'),
// НЕ '/clickhouse/tables/...' (тот появится только после CREATE TABLE
// ReplicatedMergeTree, см. phaseSchema).
func phaseCluster(ctx context.Context, coord clickhouse.Conn) {
	fmt.Println("\n=== ТОПОЛОГИЯ КЛАСТЕРА (Step 1 брифа) ===")

	rows, err := coord.Query(ctx, fmt.Sprintf(
		"SELECT shard_num, replica_num, host_name FROM system.clusters WHERE cluster = '%s' ORDER BY shard_num, replica_num", clusterName))
	if err != nil {
		log.Fatalf("query system.clusters: %v", err)
	}
	var got []clusterRow
	for rows.Next() {
		var r clusterRow
		if err := rows.Scan(&r.shardNum, &r.replicaNum, &r.hostName); err != nil {
			log.Fatalf("scan system.clusters row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate system.clusters: %v", err)
	}
	rows.Close()

	fmt.Printf("[cluster] system.clusters WHERE cluster='%s':\n", clusterName)
	for _, r := range got {
		fmt.Printf("  shard=%d replica=%d host=%s\n", r.shardNum, r.replicaNum, r.hostName)
	}
	assertFailFast(len(got) == 4, "system.clusters содержит 4 строки (2 шарда x 2 реплики), получено %d", len(got))

	wantHosts := map[string]bool{"ch-s1-r1": true, "ch-s1-r2": true, "ch-s2-r1": true, "ch-s2-r2": true}
	shardOf := map[string]uint32{}
	for _, r := range got {
		if !wantHosts[r.hostName] {
			log.Fatalf("неожиданный host_name в system.clusters: %s", r.hostName)
		}
		shardOf[r.hostName] = r.shardNum
	}
	assertFailFast(shardOf["ch-s1-r1"] == shardOf["ch-s1-r2"], "ch-s1-r1 и ch-s1-r2 в одном шарде: %d == %d", shardOf["ch-s1-r1"], shardOf["ch-s1-r2"])
	assertFailFast(shardOf["ch-s2-r1"] == shardOf["ch-s2-r2"], "ch-s2-r1 и ch-s2-r2 в одном шарде: %d == %d", shardOf["ch-s2-r1"], shardOf["ch-s2-r2"])
	assertFailFast(shardOf["ch-s1-r1"] != shardOf["ch-s2-r1"], "шард 1 (%d) и шард 2 (%d) — разные номера", shardOf["ch-s1-r1"], shardOf["ch-s2-r1"])

	var zkRootNames []string
	zkRows, err := coord.Query(ctx, "SELECT name FROM system.zookeeper WHERE path = '/'")
	if err != nil {
		log.Fatalf("query system.zookeeper root: %v", err)
	}
	for zkRows.Next() {
		var name string
		if err := zkRows.Scan(&name); err != nil {
			log.Fatalf("scan system.zookeeper row: %v", err)
		}
		zkRootNames = append(zkRootNames, name)
	}
	if err := zkRows.Err(); err != nil {
		log.Fatalf("iterate system.zookeeper: %v", err)
	}
	zkRows.Close()

	fmt.Printf("[cluster] SELECT * FROM system.zookeeper WHERE path='/': %v\n", zkRootNames)
	assertFailFast(len(zkRootNames) > 0, "system.zookeeper WHERE path='/' вернул непустой список znode — Keeper (keeper1) достижим: %v", zkRootNames)
}

// phaseSchema — Step 2 брифа: создаёт ReplicatedMergeTree (events, ON
// CLUSTER — резолвится в demo.events на каждой из 4 нод, путь Keeper
// '/clickhouse/tables/{shard}/events' с macros per-нода) и Distributed
// (events_distributed, sharding key cityHash64(user_id)) поверх кластера.
// Затем проверяет через system.zookeeper, что Keeper реально знает про ОБЕ
// реплики каждого шарда (путь /clickhouse/tables/{N}/events/replicas) —
// не просто "DDL не упал", а "координация в Keeper видна снаружи".
func phaseSchema(ctx context.Context, coord clickhouse.Conn) {
	fmt.Println("\n=== СОЗДАНИЕ РЕПЛИЦИРОВАННОЙ + DISTRIBUTED СХЕМЫ (Step 2 брифа) ===")

	if err := createReplicatedTable(ctx, coord, tblEvents); err != nil {
		log.Fatalf("create replicated table: %v", err)
	}
	fmt.Printf("[schema] demo.%s ON CLUSTER %s — ReplicatedMergeTree('/clickhouse/tables/{shard}/events', '{replica}') создана на всех 4 нодах\n", tblEvents, clusterName)

	if err := createDistributedTable(ctx, coord, tblDistributed, tblEvents); err != nil {
		log.Fatalf("create distributed table: %v", err)
	}
	fmt.Printf("[schema] demo.%s ON CLUSTER %s — Distributed(%s, demo, %s, cityHash64(user_id)) создана на всех 4 нодах\n", tblDistributed, clusterName, clusterName, tblEvents)

	for _, shard := range []string{"1", "2"} {
		zkPath := fmt.Sprintf("/clickhouse/tables/%s/events/replicas", shard)
		rows, err := coord.Query(ctx, fmt.Sprintf("SELECT name FROM system.zookeeper WHERE path = '%s' ORDER BY name", zkPath))
		if err != nil {
			log.Fatalf("query system.zookeeper %s: %v", zkPath, err)
		}
		var replicas []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				log.Fatalf("scan replicas row: %v", err)
			}
			replicas = append(replicas, name)
		}
		if err := rows.Err(); err != nil {
			log.Fatalf("iterate replicas: %v", err)
		}
		rows.Close()
		fmt.Printf("[schema] system.zookeeper path='%s': %v\n", zkPath, replicas)
		assertFailFast(len(replicas) == 2, "шард %s: Keeper знает про 2 реплики (%s): %v", shard, zkPath, replicas)
	}
}
