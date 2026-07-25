// Command distributed — стенд #5 серии "ClickHouse: глубокое погружение":
// распределённый ClickHouse (2 шарда x 2 реплики) + ClickHouse Keeper.
//
//   - Step 1: топология кластера (system.clusters) + связность с Keeper
//     (system.zookeeper).
//   - Step 2: ReplicatedMergeTree + Distributed(events_cluster, demo,
//     events, cityHash64(user_id)), реальное распределение по шардам
//     (shardNum()), репликация "первичная реплика -> вторая реплика".
//   - Step 3: вставка напрямую в одну реплику + наблюдение появления на
//     второй через Keeper; дедупликация повторной вставки идентичного
//     блока.
//   - Step 4: отказ узла — ОДНА реплика шарда недоступна (полный результат,
//     без потерь) vs ВЕСЬ шард недоступен (ошибка без skip_unavailable_shards,
//     частичный результат с ним). Docker stop/start — СНАРУЖИ этого
//     процесса (см. ../ops/distributed-demo.sh), фазы failover читают/пишут
//     JSON-снимок состояния (-state) между отдельными invocations.
//
// Запуск (из контейнера golang:1.25, сеть clickhouse-cluster-net, см.
// ../ops/distributed-demo.sh за полный live-сценарий с docker stop/start):
//
//	docker run --rm --network clickhouse-cluster-net \
//	  -v "$(pwd)/distributed:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
//	  go run . -phase=setup -csv=/data/events-distributed.csv -expect-rows=500000 \
//	  -state=/data/distributed-state.json
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	phase := flag.String("phase", "setup", "cluster|schema|insert-dist|replication|baseline|after-replica-down|after-shard-down|after-restart-verify|setup")
	coordAddr := flag.String("ch-addr", "ch-s1-r1:9000", "координатор — CH-нода, НИКОГДА не останавливаемая в failover-сценарии (Step 4 брифа)")
	csvPath := flag.String("csv", "/data/events-distributed.csv", "путь к CSV-датасету (../dataset/main.go), для -phase=insert-dist|setup")
	batchSize := flag.Int("batch", 100_000, "размер батча вставки через Distributed")
	expectRows := flag.Int64("expect-rows", 0, "если >0, ассерт: число строк insert-dist == это значение")
	statePath := flag.String("state", "/data/distributed-state.json", "путь к JSON-снимку baseline-состояния (между отдельными invocations вокруг docker stop/start)")

	flag.Parse()

	ctx := context.Background()
	coord, err := openConn(*coordAddr)
	if err != nil {
		log.Fatalf("open coordinator %s: %v", *coordAddr, err)
	}
	defer coord.Close()
	if err := coord.Ping(ctx); err != nil {
		log.Fatalf("ping coordinator %s: %v", *coordAddr, err)
	}

	switch *phase {
	case "cluster":
		phaseCluster(ctx, coord)
	case "schema":
		phaseSchema(ctx, coord)
	case "insert-dist":
		nodes, err := openAllNodes()
		if err != nil {
			log.Fatalf("open all nodes: %v", err)
		}
		defer closeAll(nodes)
		phaseInsertDist(ctx, coord, nodes, *csvPath, *batchSize, *expectRows)
	case "replication":
		nodes, err := openAllNodes()
		if err != nil {
			log.Fatalf("open all nodes: %v", err)
		}
		defer closeAll(nodes)
		phaseReplication(ctx, nodes)
	case "baseline":
		phaseBaseline(ctx, coord, *statePath)
	case "after-replica-down":
		phaseAfterReplicaDown(ctx, coord, *statePath)
	case "after-shard-down":
		phaseAfterShardDown(ctx, coord, *statePath)
	case "after-restart-verify":
		phaseAfterRestartVerify(ctx, coord, *statePath)
	case "setup":
		phaseCluster(ctx, coord)
		phaseSchema(ctx, coord)
		nodes, err := openAllNodes()
		if err != nil {
			log.Fatalf("open all nodes: %v", err)
		}
		defer closeAll(nodes)
		phaseInsertDist(ctx, coord, nodes, *csvPath, *batchSize, *expectRows)
		phaseReplication(ctx, nodes)
		phaseBaseline(ctx, coord, *statePath)
		fmt.Println("\n[distributed] setup завершён (cluster+schema+insert-dist+replication+baseline). Failover-сценарий (Step 4) — отдельные фазы вокруг docker stop/start, см. ops/distributed-demo.sh")
	default:
		fmt.Fprintf(os.Stderr, "unknown -phase=%s\n", *phase)
		os.Exit(2)
	}
}
