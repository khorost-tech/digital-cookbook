#!/usr/bin/env bash
# distributed-demo.sh — оркестрирует стенд #5 серии "ClickHouse: глубокое
# погружение": распределённый кластер (2 шарда x 2 реплики) + ClickHouse
# Keeper — шардирование, репликация, дедупликация, отказ узла.
#
# Шаги:
#   1. поднять compose/cluster.yml (4 CH-ноды + keeper1), дождаться healthy
#      всех 5 контейнеров
#   2. сгенерировать выделенный датасет ../dataset/main.go -rows=500000 в
#      dataset/out/events-distributed.csv (если уже существует — не
#      пересоздаётся, см. -force)
#   3. Go (distributed): -phase=setup —
#        cluster (system.clusters + system.zookeeper)
#      -> schema (ReplicatedMergeTree ON CLUSTER + Distributed ON CLUSTER,
#         проверка Keeper-путей /clickhouse/tables/{shard}/events/replicas)
#      -> insert-dist (вставка через Distributed, insert_distributed_sync=1,
#         распределение по шардам через shardNum(), опрос до сходимости
#         обеих реплик каждого шарда)
#      -> replication (прямая вставка в ОДНУ реплику + наблюдение появления
#         на второй через Keeper; дедупликация повторной идентичной вставки)
#      -> baseline (снимок shard1/shard2/total в JSON — точка отсчёта для
#         failover-сценария ниже)
#   4. failover-сценарий (СНАРУЖИ Go-процесса — docker stop/start):
#        docker stop ch-s1-r2 (ОДНА реплика шарда 1)
#          -> Go -phase=after-replica-down (полный результат, без потерь)
#        docker start ch-s1-r2, дождаться healthy
#        docker stop ch-s2-r1 ch-s2-r2 (ОБЕ реплики шарда 2 — шард целиком)
#          -> Go -phase=after-shard-down (ошибка БЕЗ skip_unavailable_shards,
#             частичный результат С skip_unavailable_shards=1)
#        docker start ch-s2-r1 ch-s2-r2, дождаться healthy
#          -> Go -phase=after-restart-verify (полное восстановление)
#      со всеми программными ассертами (fail-loud) на каждом шаге
#
# Запуск: bash ops/distributed-demo.sh [-rows=500000] [-force]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=500000
FORCE=0
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -force) FORCE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE="compose/cluster.yml"
CSV="dataset/out/events-distributed.csv"
STATE="dataset/out/distributed-state.json"

wait_healthy() {
  local container="$1"
  echo "[distributed-demo] ожидание healthy ($container)..."
  for i in $(seq 1 40); do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$container" 2>/dev/null || echo "unknown")
    if [ "$status" = "healthy" ]; then
      echo "[distributed-demo] $container: healthy"
      return 0
    fi
    if [ "$i" -eq 40 ]; then
      echo "[distributed-demo] $container не стал healthy за отведённое время (последний статус: $status)" >&2
      exit 1
    fi
    sleep 3
  done
}

run_go() {
  docker run --rm --network clickhouse-cluster-net \
    -v "$(pwd)/distributed:/app" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
    go run . "$@"
}

echo "[distributed-demo] up -d ($COMPOSE)"
docker compose -f "$COMPOSE" up -d

wait_healthy clickhouse-cluster-keeper1
wait_healthy clickhouse-cluster-s1-r1
wait_healthy clickhouse-cluster-s1-r2
wait_healthy clickhouse-cluster-s2-r1
wait_healthy clickhouse-cluster-s2-r2

if [ "$FORCE" -eq 1 ] || [ ! -f "$CSV" ]; then
  echo "[distributed-demo] генерация датасета: -rows=$ROWS -seed=42 -> $CSV"
  mkdir -p dataset/out
  docker run --rm -v "$(pwd)/dataset:/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
    go run . -rows="$ROWS" -seed=42 -out="/app/out/events-distributed.csv"
else
  echo "[distributed-demo] $CSV уже существует, пропускаю генерацию (используйте -force для пересоздания)"
fi

mkdir -p .gocache

echo "[distributed-demo] Go: distributed -phase=setup (-expect-rows=$ROWS)"
run_go -phase=setup -csv=/data/events-distributed.csv -expect-rows="$ROWS" -state=/data/distributed-state.json

echo ""
echo "[distributed-demo] === FAILOVER-СЦЕНАРИЙ (Step 4 брифа) ==="

echo "[distributed-demo] docker stop clickhouse-cluster-s1-r2 (ОДНА реплика шарда 1)"
docker stop clickhouse-cluster-s1-r2
sleep 2
run_go -phase=after-replica-down -state=/data/distributed-state.json

echo "[distributed-demo] docker start clickhouse-cluster-s1-r2 (восстановление)"
docker start clickhouse-cluster-s1-r2
wait_healthy clickhouse-cluster-s1-r2

echo "[distributed-demo] docker stop clickhouse-cluster-s2-r1 clickhouse-cluster-s2-r2 (ОБЕ реплики шарда 2)"
docker stop clickhouse-cluster-s2-r1 clickhouse-cluster-s2-r2
sleep 2
run_go -phase=after-shard-down -state=/data/distributed-state.json

echo "[distributed-demo] docker start clickhouse-cluster-s2-r1 clickhouse-cluster-s2-r2 (восстановление)"
docker start clickhouse-cluster-s2-r1 clickhouse-cluster-s2-r2
wait_healthy clickhouse-cluster-s2-r1
wait_healthy clickhouse-cluster-s2-r2

run_go -phase=after-restart-verify -state=/data/distributed-state.json

echo ""
echo "[distributed-demo] готово, все 4 CH-ноды + keeper1 снова healthy. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE down -v' по завершении сверки README."
