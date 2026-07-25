#!/usr/bin/env bash
# mergetree-demo.sh — оркестрирует стенд #2 серии "ClickHouse: глубокое
# погружение": MergeTree из Go и Java — вставки и запросы.
#
# Шаги:
#   1. поднять compose/compose.yml (CH single + PG16, PG не нужен этому
#      стенду, но общий compose один на серию), дождаться healthy
#   2. сгенерировать выделенный датасет ../dataset/main.go -rows=5000000 в
#      dataset/out/events-mergetree.csv (если уже существует — не
#      пересоздаётся, см. -force)
#   3. Go (go/mergetree): -phase=all — schema → load (батч 100k) → granules
#      (EXPLAIN indexes=1 + read_rows) → antipattern (батч vs построчная) →
#      async (серверная буферизация + видимость), с ассертами (fail-loud)
#   4. Java (java/mergetree): JDBC batch + client-v2 batch + гранульный
#      чек, с ассертами
#
# Запуск: bash ops/mergetree-demo.sh [-rows=5000000] [-force]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=5000000
FORCE=0
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -force) FORCE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE="compose/compose.yml"
CSV="dataset/out/events-mergetree.csv"

echo "[mergetree-demo] up -d ($COMPOSE)"
docker compose -f "$COMPOSE" up -d

echo "[mergetree-demo] ожидание healthy (clickhouse-cookbook)..."
for i in $(seq 1 40); do
  status=$(docker inspect -f '{{.State.Health.Status}}' clickhouse-cookbook 2>/dev/null || echo "unknown")
  if [ "$status" = "healthy" ]; then
    echo "[mergetree-demo] clickhouse-cookbook: healthy"
    break
  fi
  if [ "$i" -eq 40 ]; then
    echo "[mergetree-demo] clickhouse-cookbook не стал healthy за отведённое время (последний статус: $status)" >&2
    exit 1
  fi
  sleep 3
done

if [ "$FORCE" -eq 1 ] || [ ! -f "$CSV" ]; then
  echo "[mergetree-demo] генерация датасета: -rows=$ROWS -seed=42 -> $CSV"
  mkdir -p dataset/out
  docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
    go run . -rows="$ROWS" -seed=42 -out="/app/out/events-mergetree.csv"
else
  echo "[mergetree-demo] $CSV уже существует, пропускаю генерацию (используйте -force для пересоздания)"
fi

mkdir -p .gocache .m2cache

echo "[mergetree-demo] Go: mergetree -phase=all (-expect-rows=$ROWS)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/go/mergetree:/app" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
  go run . -phase=all -csv=/data/events-mergetree.csv -expect-rows="$ROWS" -ch-addr=clickhouse:9000

# ВАЖНО про монтирование: монтируется ВЕСЬ каталог databases/clickhouse, а не
# только java/. Реактор java/pom.xml объявляет <module>../drivers/java</module>
# — модуль лежит РЯДОМ с java/, вне его. Если смонтировать только "$(pwd)/java",
# Maven не найдёт ../drivers/java и Java-часть демо молча не соберётся.
# Поэтому корень стенда → /app, а рабочий каталог → /app/java (и /app/java/mergetree).
echo "[mergetree-demo] Java: сборка mergetree"
docker run --rm -v "$(pwd):/app" -v "$(pwd)/.m2cache:/root/.m2" -w /app/java maven:3.9-eclipse-temurin-25 \
  mvn -q -f pom.xml -pl mergetree -am package -DskipTests

echo "[mergetree-demo] Java: запуск mergetree (100000 строк — эквивалент батча Go)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd):/app" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.m2cache:/root/.m2" -w /app/java/mergetree maven:3.9-eclipse-temurin-25 \
  java -cp target/mergetree.jar tech.khorost.clickhouse.mergetree.Main \
  /data/events-mergetree.csv jdbc:ch://clickhouse:8123/demo http://clickhouse:8123 100000

echo "[mergetree-demo] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE down -v' по завершении сверки README."
