#!/usr/bin/env bash
# drivers-bench.sh — оркестрирует стенд #3 серии "ClickHouse: глубокое
# погружение": бенчмарк драйверов (ch-go / clickhouse-go native /
# clickhouse-go database-sql / raw HTTP / clickhouse-jdbc / client-v2).
#
# Шаги:
#   1. поднять compose/compose.yml (CH single + PG16, PG не нужен этому
#      стенду), дождаться healthy
#   2. сгенерировать выделенный датасет ../dataset/main.go -rows=$M в
#      dataset/out/events-drivers.csv (если уже существует — не
#      пересоздаётся, см. -force)
#   3. Go (drivers/go): -phase=all — 4 драйвера (ch-go, clickhouse-go
#      native, clickhouse-go database/sql, raw HTTP), каждый вставляет M
#      строк в СВОЮ таблицу батчами по -batch-size, затем один и тот же
#      аналитический SELECT (throughput/latency + ассерты)
#   4. Java (drivers/java): JDBC batch + client-v2 batch, тот же M/сценарий,
#      2 ещё таблицы, локальная сверка JDBC vs client-v2
#   5. Go -phase=verify: авторитетная межъязыковая/междрайверная сверка —
#      один и тот же аналитический SELECT через ОДНО административное
#      соединение над ВСЕМИ 6 таблицами, чексумма должна совпасть везде
#
# Запуск: bash ops/drivers-bench.sh [-rows=1000000] [-batch=100000] [-force]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=1000000
BATCH=100000
FORCE=0
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -batch=*) BATCH="${arg#-batch=}" ;;
    -force) FORCE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE="compose/compose.yml"
CSV="dataset/out/events-drivers.csv"

echo "[drivers-bench] up -d ($COMPOSE)"
docker compose -f "$COMPOSE" up -d

echo "[drivers-bench] ожидание healthy (clickhouse-cookbook)..."
for i in $(seq 1 40); do
  status=$(docker inspect -f '{{.State.Health.Status}}' clickhouse-cookbook 2>/dev/null || echo "unknown")
  if [ "$status" = "healthy" ]; then
    echo "[drivers-bench] clickhouse-cookbook: healthy"
    break
  fi
  if [ "$i" -eq 40 ]; then
    echo "[drivers-bench] clickhouse-cookbook не стал healthy за отведённое время (последний статус: $status)" >&2
    exit 1
  fi
  sleep 3
done

docker exec clickhouse-cookbook clickhouse-client -q "CREATE DATABASE IF NOT EXISTS demo"

if [ "$FORCE" -eq 1 ] || [ ! -f "$CSV" ]; then
  echo "[drivers-bench] генерация датасета: -rows=$ROWS -seed=42 -> $CSV"
  mkdir -p dataset/out
  docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
    go run . -rows="$ROWS" -seed=42 -out="/app/out/events-drivers.csv"
else
  echo "[drivers-bench] $CSV уже существует, пропускаю генерацию (используйте -force для пересоздания)"
fi

mkdir -p .gocache .m2cache

echo "[drivers-bench] Go: drivers -phase=all (M=$ROWS, batch=$BATCH) — ch-go / clickhouse-go native / database-sql / raw HTTP"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/drivers/go:/app" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
  go run . -phase=all -csv=/data/events-drivers.csv -expect-rows="$ROWS" -batch-size="$BATCH" \
  -ch-addr=clickhouse:9000 -http-addr=http://clickhouse:8123 \
  -dsn="clickhouse://default:@clickhouse:9000/demo"

echo "[drivers-bench] Java: сборка drivers"
docker run --rm -v "$(pwd)/java:/app/java" -v "$(pwd)/drivers:/app/drivers" -v "$(pwd)/.m2cache:/root/.m2" -w /app/java maven:3.9-eclipse-temurin-25 \
  mvn -q -f pom.xml -pl ../drivers/java -am package -DskipTests

echo "[drivers-bench] Java: запуск drivers (M=$ROWS, batch=$BATCH) — clickhouse-jdbc / client-v2"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/java:/app/java" -v "$(pwd)/drivers:/app/drivers" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.m2cache:/root/.m2" -w /app/drivers/java maven:3.9-eclipse-temurin-25 \
  java -cp target/drivers.jar tech.khorost.clickhouse.drivers.Main \
  /data/events-drivers.csv jdbc:ch://clickhouse:8123/demo http://clickhouse:8123 "$ROWS" "$BATCH"

echo "[drivers-bench] Go: drivers -phase=verify — сверка ВСЕХ 6 таблиц драйверов (4 Go + 2 Java)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/drivers/go:/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
  go run . -phase=verify -ch-addr=clickhouse:9000 -expect-rows="$ROWS"

echo "[drivers-bench] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE down -v' по завершении сверки README."
