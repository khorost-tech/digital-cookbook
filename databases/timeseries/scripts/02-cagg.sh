#!/usr/bin/env bash
#
# 02-cagg.sh — continuous aggregate: что он показывает и чего не показывает.
#
# Часовой агрегат над гипертаблицей cpu (14 суток × 50 рядов × 1/мин). Снимается:
#   - materialized_only по умолчанию и свежие данные после последнего refresh;
#   - опоздавшие данные в уже материализованном часе и окно политики start_offset;
#   - drop_chunks сырых данных и refresh по области, где сырых данных больше нет.
#
# Все моменты времени отсчитываются от h — начала часа на момент запуска,
# записанного в таблицу anchor, чтобы команды в фикстуре не зависели от часов.
#
#   bash scripts/02-cagg.sh   → fixtures/02-cagg.txt
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=02-cagg
fixture_header "$f"
fresh ts

sql "CREATE TABLE anchor AS SELECT date_trunc('hour', now()) AS h" >/dev/null
iso() { sql "SELECT to_char(($1) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') FROM anchor"; }
H="(SELECT h FROM anchor)"
# CALL не принимает подзапросы — границы refresh передаются литералами.
lit() { sql "SELECT quote_literal(($1)::text) FROM anchor"; }

capture "$f" "схема" "$(psql_cmd "
CREATE TABLE cpu (time timestamptz NOT NULL, series_id int NOT NULL, value double precision NOT NULL);
SELECT create_hypertable('cpu', by_range('time', INTERVAL '1 day'));")"
capture "$f" "загрузка: 14 суток × 50 рядов, конец — h минус 2 часа" \
    "$GEN ts -table cpu -shape gauge2 -series 50 -span 336h -step 1m -end $(iso "h - INTERVAL '2 hours'")"

capture "$f" "continuous aggregate по часам" "$(psql_cmd "
CREATE MATERIALIZED VIEW cpu_hourly WITH (timescaledb.continuous) AS
SELECT time_bucket('1 hour', time) AS bucket, series_id, avg(value) AS avg, count(*) AS n
FROM cpu GROUP BY bucket, series_id WITH NO DATA;")"
capture "$f" "materialized_only по умолчанию" "$(psql_cmd "SELECT view_name, materialized_only FROM timescaledb_information.continuous_aggregates")"
capture "$f" "refresh до h минус 2 часа" "$(psql_cmd "CALL refresh_continuous_aggregate('cpu_hourly', NULL, $(lit "h - INTERVAL '2 hours'"))")"

# Свежие данные: час [h-2ч, h-1ч) приходит после refresh.
capture "$f" "свежий час после refresh" \
    "$GEN ts -table cpu -shape gauge2 -series 50 -span 1h -step 1m -end $(iso "h - INTERVAL '1 hour'")"
LAST="SELECT max(bucket) AS last_bucket, count(*) AS rows FROM cpu_hourly"
RAWLAST="SELECT max(time_bucket('1 hour', time)) AS last_bucket_raw FROM cpu"
capture "$f" "агрегат: последний час (materialized_only = true)" "$(psql_cmd "$LAST")"
capture "$f" "сырые данные: последний час" "$(psql_cmd "$RAWLAST")"
capture "$f" "real-time aggregation: materialized_only = false" "$(psql_cmd "ALTER MATERIALIZED VIEW cpu_hourly SET (timescaledb.materialized_only = false)")"
capture "$f" "агрегат: последний час (materialized_only = false)" "$(psql_cmd "$LAST")"

# Опоздавшие данные: 60 точек со значением 1000 в час h-3 суток у ряда 0 —
# этот час уже материализован.
LATE_T="$H - INTERVAL '3 days'"
capture "$f" "опоздавшие данные в материализованный час" "$(psql_cmd "INSERT INTO cpu SELECT $LATE_T + i * INTERVAL '1 minute' + INTERVAL '30 seconds', 0, 1000 FROM generate_series(0, 59) i")"
CMP="SELECT (SELECT round(avg::numeric, 2) FROM cpu_hourly WHERE series_id = 0 AND bucket = $LATE_T) AS in_aggregate, (SELECT round(avg(value)::numeric, 2) FROM cpu WHERE series_id = 0 AND time >= $LATE_T AND time < $LATE_T + INTERVAL '1 hour') AS from_raw"
capture "$f" "час h-3 суток: агрегат против сырых данных" "$(psql_cmd "$CMP")"
capture "$f" "политика обновления: окно 2 суток" "$(psql_cmd "SELECT add_continuous_aggregate_policy('cpu_hourly', start_offset => INTERVAL '2 days', end_offset => INTERVAL '1 hour', schedule_interval => INTERVAL '1 hour') AS job_id")"
JOB="$(sql "SELECT job_id FROM timescaledb_information.jobs WHERE proc_name = 'policy_refresh_continuous_aggregate' AND hypertable_name = 'cpu_hourly'")"
capture "$f" "прогон политики" "$(psql_cmd "CALL run_job($JOB)")"
capture "$f" "час h-3 суток после прогона политики" "$(psql_cmd "$CMP")"
capture "$f" "ручной refresh этого часа" "$(psql_cmd "CALL refresh_continuous_aggregate('cpu_hourly', $(lit "h - INTERVAL '3 days'"), $(lit "h - INTERVAL '3 days' + INTERVAL '1 hour'"))")"
capture "$f" "час h-3 суток после ручного refresh" "$(psql_cmd "$CMP")"

# Ретеншн сырых данных и refresh по опустевшей области.
OLD="SELECT count(*) AS aggregate_rows FROM cpu_hourly WHERE bucket < date_trunc('day', $H) - INTERVAL '10 days'"
capture "$f" "агрегат: строки старше 10 суток до drop_chunks" "$(psql_cmd "$OLD")"
capture "$f" "drop_chunks сырых данных старше 10 суток" "$(psql_cmd "SELECT count(*) AS dropped FROM drop_chunks('cpu', older_than => date_trunc('day', $H) - INTERVAL '10 days')")"
capture "$f" "агрегат: строки старше 10 суток после drop_chunks" "$(psql_cmd "$OLD")"
capture "$f" "refresh по всему диапазону" "$(psql_cmd "CALL refresh_continuous_aggregate('cpu_hourly', NULL, $(lit "h - INTERVAL '1 hour'"))")"
capture "$f" "агрегат: строки старше 10 суток после refresh" "$(psql_cmd "$OLD")"
# Агрегат без начального refresh: только политика с окном 2 суток.
capture "$f" "второй агрегат: WITH NO DATA и только политика" "$(psql_cmd "
CREATE MATERIALIZED VIEW cpu_hourly_policy WITH (timescaledb.continuous) AS
SELECT time_bucket('1 hour', time) AS bucket, series_id, avg(value) AS avg
FROM cpu GROUP BY bucket, series_id WITH NO DATA;
SELECT add_continuous_aggregate_policy('cpu_hourly_policy', start_offset => INTERVAL '2 days', end_offset => INTERVAL '1 hour', schedule_interval => INTERVAL '1 hour') AS job_id;")"
JOB2="$(sql "SELECT job_id FROM timescaledb_information.jobs WHERE proc_name = 'policy_refresh_continuous_aggregate' AND hypertable_name = 'cpu_hourly_policy'")"
capture "$f" "прогон политики второго агрегата" "$(psql_cmd "CALL run_job($JOB2)")"
capture "$f" "второй агрегат против сырых данных: глубина истории" "$(psql_cmd "SELECT (SELECT min(bucket) FROM cpu_hourly_policy) AS aggregate_from, (SELECT count(DISTINCT bucket) FROM cpu_hourly_policy) AS aggregate_hours, (SELECT min(time_bucket('1 hour', time)) FROM cpu) AS raw_from, (SELECT count(DISTINCT time_bucket('1 hour', time)) FROM cpu) AS raw_hours")"
log "записано: $FIXTURES_DIR/$f.txt"
