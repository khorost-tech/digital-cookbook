#!/usr/bin/env bash
# when-olap-demo.sh — оркестрирует стенд #1 серии "ClickHouse: глубокое
# погружение": когда OLAP (ClickHouse), а когда достаточно PostgreSQL.
#
# Шаги:
#   1. поднять compose/compose.yml (CH single + PG16), дождаться healthy
#   2. сгенерировать общий датасет (../dataset/main.go) в dataset/out/events.csv
#      (детерминирован seed'ом — при повторном запуске с тем же -rows файл
#      не пересоздаётся, если уже существует; -force пересоздаёт)
#   3. собрать/запустить ../when-olap (Go) фазой -phase=all внутри контейнера
#      golang:1.25 на сети clickhouse-cookbook-net: schema → load → size →
#      aggregate (с индексом/без) → point-lookup → mutation-демо, с
#      программными ассертами (fail-loud)
#
# Запуск: bash ops/when-olap-demo.sh [-rows=20000000] [-force]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд, чтобы можно было руками дозадать SQL по живым
# данным сразу после прогона.

set -euo pipefail
export MSYS_NO_PATHCONV=1        # Git Bash on Windows: не переписывать unix-style пути в docker-аргументах
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=20000000
FORCE=0
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -force) FORCE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE="compose/compose.yml"
CSV="dataset/out/events.csv"

echo "[when-olap-demo] up -d ($COMPOSE)"
docker compose -f "$COMPOSE" up -d

echo "[when-olap-demo] ожидание healthy..."
for svc in clickhouse-cookbook clickhouse-cookbook-postgres; do
  for i in $(seq 1 40); do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$svc" 2>/dev/null || echo "unknown")
    if [ "$status" = "healthy" ]; then
      echo "[when-olap-demo] $svc: healthy"
      break
    fi
    if [ "$i" -eq 40 ]; then
      echo "[when-olap-demo] $svc не стал healthy за отведённое время (последний статус: $status)" >&2
      exit 1
    fi
    sleep 3
  done
done

if [ "$FORCE" -eq 1 ] || [ ! -f "$CSV" ]; then
  echo "[when-olap-demo] генерация датасета: -rows=$ROWS -seed=42 -> $CSV"
  mkdir -p dataset/out
  docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
    go run . -rows="$ROWS" -seed=42 -out="/app/out/events.csv"
else
  echo "[when-olap-demo] $CSV уже существует, пропускаю генерацию (используйте -force для пересоздания)"
fi

ACTUAL_ROWS=$(($(wc -l < "$CSV") - 1))
echo "[when-olap-demo] датасет: $ACTUAL_ROWS строк (без заголовка)"

echo "[when-olap-demo] прогон стенда when-olap (-phase=all, -expect-rows=$ACTUAL_ROWS)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/when-olap:/app" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
  go run . -phase=all -csv=/data/events.csv -expect-rows="$ACTUAL_ROWS" -runs=5 \
  -ch-addr=clickhouse:9000 -pg-dsn="postgres://postgres:chdemo@postgres:5432/demo"

echo "[when-olap-demo] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE down -v' по завершении сверки README."
