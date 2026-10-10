#!/usr/bin/env bash
#
# 01-chunks.sh — гипертаблица против обычной таблицы на одних данных.
#
# 30 суток × 200 рядов × точка в минуту = 8,64 млн строк, чанк — сутки.
# Снимается: сколько чанков читает запрос «за последний час» и за вчерашние
# сутки, записанные двумя способами — диапазоном по самой колонке time и
# условием через функцию от неё; и чем ретеншн по DELETE отличается от
# drop_chunks.
#
#   bash scripts/01-chunks.sh   → fixtures/01-chunks.txt
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=01-chunks
fixture_header "$f"
fresh ts

sql "CREATE TABLE anchor AS SELECT date_trunc('minute', now()) AS h" >/dev/null
END="$(sql "SELECT to_char(h AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') FROM anchor")"

capture "$f" "схема: гипертаблица и обычная таблица" "$(psql_cmd "
CREATE TABLE metrics (time timestamptz NOT NULL, series_id int NOT NULL, value double precision NOT NULL);
SELECT create_hypertable('metrics', by_range('time', INTERVAL '1 day'));
CREATE INDEX ON metrics (series_id, time DESC);
CREATE TABLE metrics_plain (LIKE metrics);
CREATE INDEX ON metrics_plain (time DESC);
CREATE INDEX ON metrics_plain (series_id, time DESC);")"

for t in metrics metrics_plain; do
    capture "$f" "загрузка: $t, 30 суток × 200 рядов × 1/мин" \
        "$GEN ts -table $t -shape gauge2 -series 200 -span 720h -step 1m -end $END"
done
sql "VACUUM ANALYZE metrics; VACUUM ANALYZE metrics_plain" >/dev/null

capture "$f" "чанки гипертаблицы" "$(psql_cmd "SELECT count(*) AS chunks, min(range_start) AS first, max(range_end) AS last FROM timescaledb_information.chunks WHERE hypertable_name = 'metrics'")"

# Последний час: now() — не константа, отсечение происходит при старте исполнения.
Q1="SELECT avg(value) FROM metrics WHERE time > now() - INTERVAL '1 hour'"
capture "$f" "последний час: план" "$(psql_cmd "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) $Q1")"
capture "$f" "последний час: сколько чанков сканируется" "$(psql_cmd "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) $Q1") | grep -E 'Chunks excluded|_hyper_'"

# Вчерашние сутки — диапазоном по колонке и условием через функцию от неё.
Q2="SELECT avg(value) FROM metrics WHERE time >= date_trunc('day', now()) - INTERVAL '1 day' AND time < date_trunc('day', now())"
Q3="SELECT avg(value) FROM metrics WHERE date_trunc('day', time) = date_trunc('day', now()) - INTERVAL '1 day'"
Q4="SELECT avg(value) FROM metrics WHERE time::date = current_date - 1"
for q in Q2 Q3 Q4; do
    capture "$f" "вчерашние сутки ($q): число чанков в плане" \
        "$(psql_cmd "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) ${!q}") | grep -E 'Chunks excluded|Seq Scan|Index Scan' | sed -E 's/_hyper_[0-9]+_[0-9]+_chunk/_hyper_N_chunk/' | sort | uniq -c"
done
for q in Q2 Q3; do
    capture "$f" "вчерашние сутки ($q): время запроса" "docker exec tsdb-ts psql -U postgres -d ts -c '\\timing on' -c \"${!q}\""
done

# Ретеншн: удалить всё старше границы суток — одну и ту же область в обеих
# таблицах. Граница совпадает с границей чанка, иначе drop_chunks оставит
# частично устаревший чанк целиком.
capture "$f" "размер до ретеншна" "$(psql_cmd "SELECT pg_size_pretty(hypertable_size('metrics')) AS hypertable, pg_size_pretty(pg_total_relation_size('metrics_plain')) AS plain")"
capture "$f" "ретеншн: DELETE из обычной таблицы" "docker exec tsdb-ts psql -U postgres -d ts -c '\\timing on' -c \"DELETE FROM metrics_plain WHERE time < date_trunc('day', (SELECT h FROM anchor)) - INTERVAL '29 days'\""
capture "$f" "ретеншн: drop_chunks у гипертаблицы" "docker exec tsdb-ts psql -U postgres -d ts -c '\\timing on' -c \"SELECT count(*) FROM drop_chunks('metrics', older_than => date_trunc('day', (SELECT h FROM anchor)) - INTERVAL '29 days')\""
capture "$f" "размер после ретеншна" "$(psql_cmd "SELECT pg_size_pretty(hypertable_size('metrics')) AS hypertable, pg_size_pretty(pg_total_relation_size('metrics_plain')) AS plain")"
capture "$f" "мёртвые строки после DELETE" "$(psql_cmd "SELECT relname, n_live_tup, n_dead_tup FROM pg_stat_user_tables WHERE relname = 'metrics_plain'")"
capture "$f" "строк осталось" "$(psql_cmd "SELECT (SELECT count(*) FROM metrics) AS hypertable, (SELECT count(*) FROM metrics_plain) AS plain")"
log "записано: $FIXTURES_DIR/$f.txt"
