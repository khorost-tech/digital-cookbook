#!/usr/bin/env bash
# decision-demo.sh — оркестрирует стенд #8 (финальный) серии "ClickHouse:
# глубокое погружение": карта выбора ClickHouse vs TimescaleDB vs DuckDB vs
# PostgreSQL — один и тот же аналитический сценарий (тот же датасет/агрегат,
# что стенд #1 when-olap) на четырёх системах.
#
# Шаги:
#   1. up -d compose/compose.yml (CH+PG) + compose/decision.yml (TimescaleDB),
#      дождаться healthy ВСЕХ ТРЁХ (clickhouse-cookbook,
#      clickhouse-cookbook-postgres, clickhouse-cookbook-timescaledb).
#   2. Подготовить общий CSV: усечённое (первые N строк) подмножество
#      dataset/out/events.csv (общий датасет серии, -seed=42 — байт-в-байт
#      воспроизводим) — ОДИН файл, ОДНО и то же подмножество для всех 4
#      систем, чтобы сравнение было яблоки-к-яблокам. Если events.csv ещё
#      не сгенерирован (`dataset/main.go -rows=20000000 -seed=42`, см.
#      ops/when-olap-demo.sh) — генерируется здесь заново тем же вызовом.
#   3. Go (decision): -phase=all — schema (DDL CH/PG/Timescale) -> load
#      (батч в CH, COPY в PG и Timescale, CSV->Parquet конвертация в DuckDB)
#      -> size (размер на диске 4 систем ДО TimescaleDB-компрессии) ->
#      aggregate (один и тот же агрегат на 4 системах, checksum-сверка) ->
#      timescale (continuous aggregate + native compression, обзорно), с
#      ассертами (fail-loud) на каждом шаге.
#
# Запуск: bash ops/decision-demo.sh [-rows=10000000] [-runs=5]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=10000000
RUNS=5
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -runs=*) RUNS="${arg#-runs=}" ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE_CH="compose/compose.yml"
COMPOSE_DECISION="compose/decision.yml"
FULL_CSV="dataset/out/events.csv"
DECISION_CSV="dataset/out/events-decision.csv"
DECISION_PARQUET="dataset/out/events-decision.parquet"

echo "[decision-demo] up -d ($COMPOSE_CH + $COMPOSE_DECISION timescaledb)"
docker compose -f "$COMPOSE_CH" up -d
docker compose -f "$COMPOSE_CH" -f "$COMPOSE_DECISION" up -d timescaledb

wait_healthy() {
  local name="$1"
  echo "[decision-demo] ожидание healthy ($name)..."
  for i in $(seq 1 40); do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$name" 2>/dev/null || echo "unknown")
    if [ "$status" = "healthy" ]; then
      echo "[decision-demo] $name: healthy"
      return 0
    fi
    if [ "$i" -eq 40 ]; then
      echo "[decision-demo] $name не стал healthy за отведённое время (последний статус: $status)" >&2
      exit 1
    fi
    sleep 3
  done
}
wait_healthy clickhouse-cookbook
wait_healthy clickhouse-cookbook-postgres
wait_healthy clickhouse-cookbook-timescaledb

mkdir -p .gocache dataset/out

if [ ! -f "$FULL_CSV" ]; then
  echo "[decision-demo] $FULL_CSV не найден — генерация общего датасета (dataset/main.go -rows=20000000 -seed=42)"
  docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
    go run . -rows=20000000 -seed=42 -out=/app/out/events.csv
fi

if [ ! -f "$DECISION_CSV" ]; then
  echo "[decision-demo] подготовка усечённого подмножества ($ROWS строк) из $FULL_CSV -> $DECISION_CSV"
  { head -n 1 "$FULL_CSV"; sed -n "2,$((ROWS + 1))p" "$FULL_CSV"; } > "$DECISION_CSV"
fi

echo "[decision-demo] Go: decision -phase=all (-expect-rows=$ROWS -runs=$RUNS)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/decision:/app" -v "$(pwd)/.gocache:/go/pkg/mod" -v "$(pwd)/dataset/out:/data" -w /app golang:1.25 \
  go run . -phase=all \
  -csv="/data/$(basename "$DECISION_CSV")" -parquet="/data/$(basename "$DECISION_PARQUET")" \
  -ch-addr=clickhouse:9000 \
  -pg-dsn="postgres://postgres:chdemo@postgres:5432/demo" \
  -ts-dsn="postgres://postgres:chdemo@timescaledb:5432/demo" \
  -expect-rows="$ROWS" -runs="$RUNS"

echo "[decision-demo] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE_CH -f $COMPOSE_DECISION down -v' по завершении сверки README."
