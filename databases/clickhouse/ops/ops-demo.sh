#!/usr/bin/env bash
# ops-demo.sh — оркестрирует стенд #6 серии "ClickHouse: глубокое
# погружение": эксплуатация и тюнинг single-node ClickHouse (parts/merges,
# mutations, мониторинг system.* + BACKUP/RESTORE, codec-тюнинг).
#
# Шаги:
#   1. up -d compose/compose.yml (CH single + PG16 — PG этому стенду не
#      нужен, общий compose один на серию), дождаться healthy CH.
#      compose/compose.yml монтирует ../config/backups.xml
#      (config.d/backups.xml) — allowed_path для BACKUP TABLE ... TO File(...)
#      внутри уже существующего тома clickhouse-data.
#   2. mkdir /var/lib/clickhouse/backups внутри контейнера (каталог для
#      бэкапов — сам allowed_path его не создаёт).
#   3. сгенерировать выделенный датасет ../dataset/main.go -rows=750000 в
#      dataset/out/events-ops.csv (если уже существует — не пересоздаётся,
#      см. -force). 750000 строк: 200k — ops_events (Step 1/2/3) +
#      200k — index_granularity fine/coarse (Step 4 бонус, тот же диапазон
#      0..200k) + 500k (диапазон 200k..700k) — codec ZSTD/Delta сравнение.
#   4. Go (ops-stand): -phase=all — merges (SYSTEM STOP MERGES + 200 батчей
#      по 1000 строк → START MERGES → OPTIMIZE FINAL, active parts до/после)
#      → mutations (UPDATE/DELETE по country, polling is_done с таймаутом)
#      → monitoring (query_log/parts_columns/metrics + BACKUP/RESTORE
#      round-trip) → codec (ZSTD(3) vs Delta+ZSTD(3) для event_time +
#      бонус index_granularity/max_threads/max_memory_usage), с ассертами
#      (fail-loud)
#
# Запуск: bash ops/ops-demo.sh [-rows=750000] [-force]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

ROWS=750000
FORCE=0
for arg in "$@"; do
  case "$arg" in
    -rows=*) ROWS="${arg#-rows=}" ;;
    -force) FORCE=1 ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE="compose/compose.yml"
CSV="dataset/out/events-ops.csv"

echo "[ops-demo] up -d ($COMPOSE)"
docker compose -f "$COMPOSE" up -d

echo "[ops-demo] ожидание healthy (clickhouse-cookbook)..."
for i in $(seq 1 40); do
  status=$(docker inspect -f '{{.State.Health.Status}}' clickhouse-cookbook 2>/dev/null || echo "unknown")
  if [ "$status" = "healthy" ]; then
    echo "[ops-demo] clickhouse-cookbook: healthy"
    break
  fi
  if [ "$i" -eq 40 ]; then
    echo "[ops-demo] clickhouse-cookbook не стал healthy за отведённое время (последний статус: $status)" >&2
    exit 1
  fi
  sleep 3
done

echo "[ops-demo] mkdir /var/lib/clickhouse/backups в контейнере (allowed_path из config/backups.xml)"
# docker exec по умолчанию root; сам clickhouse-server процесс работает под
# uid 101 (clickhouse) — без chown BACKUP TABLE падает Permission denied
# (найдено живьём на первом прогоне).
docker exec clickhouse-cookbook mkdir -p /var/lib/clickhouse/backups
docker exec clickhouse-cookbook chown clickhouse:clickhouse /var/lib/clickhouse/backups

if [ "$FORCE" -eq 1 ] || [ ! -f "$CSV" ]; then
  echo "[ops-demo] генерация датасета: -rows=$ROWS -seed=42 -> $CSV"
  mkdir -p dataset/out
  docker run --rm -v "$(pwd)/dataset:/app" -w /app golang:1.25 \
    go run . -rows="$ROWS" -seed=42 -out="/app/out/events-ops.csv"
else
  echo "[ops-demo] $CSV уже существует, пропускаю генерацию (используйте -force для пересоздания)"
fi

mkdir -p .gocache

echo "[ops-demo] Go: ops-stand -phase=all"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/ops-stand:/app" -v "$(pwd)/dataset/out:/data" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
  go run . -phase=all -csv=/data/events-ops.csv -ch-addr=clickhouse:9000

echo "[ops-demo] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE down -v' по завершении сверки README."
