#!/usr/bin/env bash
#
# 06-editions.sh — что есть в бесплатных сборках.
#
# TimescaleDB поставляется в двух сборках одной версии: с лицензией Timescale
# License (TSL; образ без суффикса) и Apache-2.0 (образ -oss). Проверяется, что
# из сценариев 01–03 работает в -oss. У VictoriaMetrics проверяются флаги
# даунсэмплинга и ретеншн-фильтров в открытой сборке, у Prometheus — есть ли
# даунсэмплинг вообще.
#
#   bash scripts/06-editions.sh   → fixtures/06-editions.txt
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=06-editions
fixture_header "$f"
fresh ts
$COMPOSE --profile oss rm -sf ts-oss >/dev/null 2>&1
$COMPOSE --profile oss up -d --wait ts-oss >/dev/null 2>&1 || fail "ts-oss не поднялся"

OSS="docker exec tsdb-ts-oss psql -U postgres -d ts -c"
for c in tsdb-ts tsdb-ts-oss; do
    capture "$f" "$c: лицензия сборки" "docker exec $c psql -U postgres -d ts -c 'SHOW timescaledb.license'"
done
capture "$f" "oss: гипертаблица" "$OSS \"CREATE TABLE cpu (time timestamptz NOT NULL, series_id int NOT NULL, value double precision NOT NULL); SELECT create_hypertable('cpu', by_range('time', INTERVAL '1 day'));\""
capture "$f" "oss: данные за 3 суток" "$OSS \"INSERT INTO cpu SELECT t, 1, random() FROM generate_series(now() - INTERVAL '3 days', now(), INTERVAL '1 minute') t\""
capture "$f" "oss: drop_chunks" "$OSS \"SELECT count(*) FROM drop_chunks('cpu', older_than => now() - INTERVAL '2 days')\""
capture "$f" "oss: continuous aggregate" "$OSS \"CREATE MATERIALIZED VIEW cpu_hourly WITH (timescaledb.continuous) AS SELECT time_bucket('1 hour', time) AS bucket, avg(value) FROM cpu GROUP BY bucket WITH NO DATA\""
capture "$f" "oss: сжатие" "$OSS \"ALTER TABLE cpu SET (timescaledb.compress, timescaledb.compress_segmentby = 'series_id')\""
capture "$f" "oss: политика ретеншна" "$OSS \"SELECT add_retention_policy('cpu', INTERVAL '30 days')\""
capture "$f" "oss: time_bucket" "$OSS \"SELECT count(DISTINCT time_bucket('1 hour', time)) FROM cpu\""

capture "$f" "VictoriaMetrics: даунсэмплинг" "docker run --rm victoriametrics/victoria-metrics:v1.153.0 -downsampling.period=30d:5m"
capture "$f" "VictoriaMetrics: ретеншн-фильтр" "docker run --rm victoriametrics/victoria-metrics:v1.153.0 -retentionFilter='{env=\"dev\"}:7d'"
capture "$f" "Prometheus: флаги про даунсэмплинг" "docker run --rm prom/prometheus:v3.15.0 --help 2>&1 | grep -ci downsampl"
$COMPOSE --profile oss rm -sf ts-oss >/dev/null 2>&1
log "записано: $FIXTURES_DIR/$f.txt"
