#!/usr/bin/env bash
# s3-demo.sh — оркестрирует стенд #7 серии "ClickHouse: глубокое
# погружение": ClickHouse и S3 — многоуровневое хранение (тиринг через
# storage_policy=hot_cold, MinIO) + табличная функция s3() (Parquet
# round-trip + ingestion).
#
# Шаги:
#   1. up -d compose/compose.yml + compose/minio.yml, дождаться healthy
#      ОБОИХ (clickhouse-cookbook, clickhouse-cookbook-minio). compose.yml
#      монтирует ../config/storage.xml БЕЗУСЛОВНО (skip_access_check=true
#      защищает старт CH, даже когда MinIO ещё не поднят другими стендами).
#   2. создать бакет chdata на MinIO (`docker run minio/mc` — mc alias set
#      + mc mb, тот же приём, что ../../opensearch/ism/README.md) — ДО
#      реальных запросов к s3-диску: сам ClickHouse бакет не создаёт.
#   3. ПЕРЕЗАПУСТИТЬ clickhouse (docker compose restart clickhouse) —
#      storage.xml был смонтирован и до этого (skip_access_check не даёт
#      упасть), но реальная проверка диска (phasePolicy) должна идти
#      против УЖЕ существующего бакета; restart — дешёвая гарантия чистого
#      состояния диска на старте, а не обязательное техническое требование.
#   4. Go (s3): -phase=all — policy (system.disks/system.storage_policies)
#      -> tiering (MergeTree storage_policy=hot_cold, вставка старой/свежей
#      партиции, ALTER TABLE ... MOVE PARTITION ... TO DISK 's3',
#      system.parts.disk_name, запрос по перемещённой партиции) -> parquet
#      (INSERT INTO FUNCTION s3(...) Parquet round-trip + ingestion S3 ->
#      MergeTree), с ассертами (fail-loud)
#
# Запуск: bash ops/s3-demo.sh [-old-rows=300000] [-fresh-rows=300000]
#
# down -v выполняется автором ОТДЕЛЬНО (после сверки README) — этот скрипт
# НЕ останавливает стенд.

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # clickhouse/

OLD_ROWS=300000
FRESH_ROWS=300000
for arg in "$@"; do
  case "$arg" in
    -old-rows=*) OLD_ROWS="${arg#-old-rows=}" ;;
    -fresh-rows=*) FRESH_ROWS="${arg#-fresh-rows=}" ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

COMPOSE_CH="compose/compose.yml"
COMPOSE_MINIO="compose/minio.yml"
BUCKET="chdata"
MINIO_USER="minioadmin"
MINIO_PASS="minioadmin123"

echo "[s3-demo] up -d ($COMPOSE_CH + $COMPOSE_MINIO)"
docker compose -f "$COMPOSE_CH" up -d
docker compose -f "$COMPOSE_CH" -f "$COMPOSE_MINIO" up -d minio

wait_healthy() {
  local name="$1"
  echo "[s3-demo] ожидание healthy ($name)..."
  for i in $(seq 1 40); do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$name" 2>/dev/null || echo "unknown")
    if [ "$status" = "healthy" ]; then
      echo "[s3-demo] $name: healthy"
      return 0
    fi
    if [ "$i" -eq 40 ]; then
      echo "[s3-demo] $name не стал healthy за отведённое время (последний статус: $status)" >&2
      exit 1
    fi
    sleep 3
  done
}
wait_healthy clickhouse-cookbook
wait_healthy clickhouse-cookbook-minio

echo "[s3-demo] создание бакета $BUCKET на MinIO (mc alias set + mc mb -p)"
docker run --rm --network clickhouse-cookbook-net --entrypoint /bin/sh \
  minio/minio:RELEASE.2025-09-07T16-13-09Z \
  -c "mc alias set chdemo http://minio:9000 $MINIO_USER $MINIO_PASS && mc mb -p chdemo/$BUCKET"

echo "[s3-demo] перезапуск clickhouse (чистое состояние s3-диска против уже существующего бакета)"
docker compose -f "$COMPOSE_CH" restart clickhouse
wait_healthy clickhouse-cookbook

mkdir -p .gocache

echo "[s3-demo] Go: s3 -phase=all (-old-rows=$OLD_ROWS -fresh-rows=$FRESH_ROWS)"
docker run --rm --network clickhouse-cookbook-net \
  -v "$(pwd)/s3:/app" -v "$(pwd)/.gocache:/go/pkg/mod" -w /app golang:1.25 \
  go run . -phase=all -ch-addr=clickhouse:9000 \
  -old-rows="$OLD_ROWS" -fresh-rows="$FRESH_ROWS" \
  -s3-endpoint="http://minio:9000/$BUCKET/" -s3-access-key="$MINIO_USER" -s3-secret-key="$MINIO_PASS"

echo "[s3-demo] готово. Стенд ОСТАВЛЕН запущенным — 'docker compose -f $COMPOSE_CH -f $COMPOSE_MINIO down -v' по завершении сверки README."
