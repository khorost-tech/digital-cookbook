#!/usr/bin/env bash
# matviews-demo.sh — оркестрирует стенд #4 серии "ClickHouse: глубокое
# погружение": материализованные представления + real-time агрегации +
# живой Kafka -> CH пайплайн.
#
# Шаги:
#   1. поднять compose/compose.yml (CH single + PG16, PG не нужен этому
#      стенду) + compose/kafka.yml (single-node KRaft), дождаться healthy
#   2. сгенерировать выделенный датасет ../dataset/main.go -rows=1000000 в
#      dataset/out/events-matviews.csv (если уже существует — не
#      пересоздаётся, см. -force)
#   3. Go (materialized-views): -phase=all —
#        agg (MV-триггер: pre-MV insert -> agg пуста -> truncate -> bulk
#             load -> сверка -Merge/finalizeAggregation vs пересчёт)
#      -> summing (SummingMergeTree)
#      -> projection (ADD PROJECTION + EXPLAIN + force_optimize_projection)
#      -> ttl (TTL DELETE на партициях, синтетические старые/свежие строки)
#      -> kafka-schema + kafka-produce (franz-go, ОГРАНИЧЕННЫЙ объём) +
#         kafka-verify (опрос с ТАЙМАУТОМ, НЕ бесконечный цикл)
#      со всеми программными ассертами (fail-loud)
#
# Запуск: bash ops/matviews-demo.sh [-rows=1000000] [-kafka-events=50000] [-force]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=1000000
KAFKA_EVENTS=50000
FORCE=0
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -kafka-events=*) KAFKA_EVENTS="${arg#-kafka-events=}" ;;
    -force) FORCE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE_CH="compose/compose.yml"
COMPOSE_KAFKA="compose/kafka.yml"
CSV="dataset/out/events-matviews.csv"

echo "[matviews-demo] up -d ($COMPOSE_CH + $COMPOSE_KAFKA)"
docker compose -f "$COMPOSE_CH" up -d
docker compose -f "$COMPOSE_CH" -f "$COMPOSE_KAFKA" up -d kafka

echo "[matviews-demo] ожидание healthy (clickhouse-cookbook)..."
for i in $(seq 1 40); do
  status=$(docker inspect -f '{{.State.Health.Status}}' clickhouse-cookbook 2>/dev/null || echo "unknown")
  if [ "$status" = "healthy" ]; then
    echo "[matviews-demo] clickhouse-cookbook: healthy"
    break
  fi
  if [ "$i" -eq 40 ]; then
    echo "[matviews-demo] clickhouse-cookbook не стал healthy за отведённое время (последний статус: $status)" >&2
    exit 1
  fi
  sleep 3
done

echo "[matviews-demo] ожидание healthy (clickhouse-cookbook-kafka)..."
for i in $(seq 1 40); do
  status=$(docker inspect -f '{{.State.Health.Status}}' clickhouse-cookbook-kafka 2>/dev/null || echo "unknown")
  if [ "$status" = "healthy" ]; then
    echo "[matviews-demo] clickhouse-cookbook-kafka: healthy"
    break
  fi
  if [ "$i" -eq 40 ]; then
    echo "[matviews-demo] clickhouse-cookbook-kafka не стал healthy за отведённое время (последний статус: $status)" >&2
    exit 1
  fi
  sleep 3
done

if [ "$FORCE" -eq 1 ] || [ ! -f "$CSV" ]; then
  echo "[matviews-demo] генерация датасета: -rows=$ROWS -seed=42 -> $CSV"
  mkdir -p dataset/out
  docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
    go run . -rows="$ROWS" -seed=42 -out="/app/out/events-matviews.csv"
else
  echo "[matviews-demo] $CSV уже существует, пропускаю генерацию (используйте -force для пересоздания)"
fi

mkdir -p .gocache

echo "[matviews-demo] Go: materialized-views -phase=all (-expect-rows=$ROWS -kafka-events=$KAFKA_EVENTS)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/materialized-views:/app" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
  go run . -phase=all -csv=/data/events-matviews.csv -expect-rows="$ROWS" \
  -ch-addr=clickhouse:9000 -kafka-brokers=kafka:9092 -kafka-broker-list-ch=kafka:9092 \
  -kafka-events="$KAFKA_EVENTS" -kafka-timeout=120s

echo "[matviews-demo] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE_CH -f $COMPOSE_KAFKA down -v' по завершении сверки README."
