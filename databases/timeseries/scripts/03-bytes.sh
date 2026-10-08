#!/usr/bin/env bash
#
# 03-bytes.sh — сколько байт на диске занимает одна точка ряда.
#
# Один и тот же набор — 200 рядов × сутки × шаг 15 с = 1 152 000 точек — в
# четырёх формах: counter (растущий целый счётчик), gauge2 (gauge, округлённый
# до 0,01), gauge (тот же gauge с полной точностью float64), const (константа).
# Каждая форма пишется в три системы:
#   - Prometheus: OpenMetrics-файл → promtool tsdb create-blocks-from openmetrics,
#     двухчасовые блоки — те же, что Prometheus пишет сам из head;
#   - VictoriaMetrics: /api/v1/import/prometheus, затем force_flush и force_merge;
#   - TimescaleDB: гипертаблица (time, series_id, value) до и после сжатия.
# В размер входят данные и индекс у всех трёх.
#
#   bash scripts/03-bytes.sh   → fixtures/03-bytes.txt, fixtures/bytes.tsv
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=03-bytes
fixture_header "$f"
TSV="$FIXTURES_DIR/bytes.tsv"
printf 'shape\tsystem\tsamples\tbytes\n' > "$TSV"
SHAPES="${SHAPES:-counter gauge2 gauge const}"
END="$(date -u '+%Y-%m-%dT%H:00:00Z')"
PROMTOOL="docker run --rm -v \"\$PWD/work:/work\" --entrypoint promtool prom/prometheus:v3.15.0"
WORKGEN="docker run --rm -v \"\$PWD/work:/work\" tsdb-gen"

rm -rf work; mkdir -p work
fresh ts

for s in $SHAPES; do
    # --- Prometheus ---
    capture "$f" "$s: OpenMetrics-файл" "$WORKGEN om -shape $s -series 200 -span 24h -step 15s -end $END -out /work/$s.om"
    capture "$f" "$s: Prometheus, блоки из файла" "$PROMTOOL tsdb create-blocks-from openmetrics -q /work/$s.om /work/prom-$s"
    capture "$f" "$s: Prometheus, блоки" "$PROMTOOL tsdb list /work/prom-$s"
    capture "$f" "$s: Prometheus, итог по блокам" "$PROMTOOL tsdb list /work/prom-$s | awk 'NR > 1 { n += \$5; b += \$8 } END { printf \"samples=%d bytes=%d bytes/sample=%.2f\\n\", n, b, b / n }'"
    printf '%s\tprometheus\t%s\n' "$s" "$(printf '%s' "$CAPTURE_OUT" | sed -E 's/samples=([0-9]+) bytes=([0-9]+).*/\1\t\2/')" >> "$TSV"
    rm -f "work/$s.om"

    # --- VictoriaMetrics ---
    fresh vm
    capture "$f" "$s: VictoriaMetrics, импорт" "$GEN vm -url http://vm:8428 -shape $s -series 200 -span 24h -step 15s -end $END"
    docker exec tsdb-vm wget -qO- http://127.0.0.1:8428/internal/force_flush >/dev/null
    docker exec tsdb-vm wget -qO- "http://127.0.0.1:8428/internal/force_merge?partition_prefix=${END:0:4}_${END:5:2}" >/dev/null
    for i in $(seq 1 60); do
        [ "$(docker exec tsdb-vm wget -qO- http://127.0.0.1:8428/metrics | awk '/^vm_active_merges\{/ { s += $2 } END { print s + 0 }')" = 0 ] && break
        sleep 1
    done
    sleep 3
    capture "$f" "$s: VictoriaMetrics, размер хранилища" "docker exec tsdb-vm wget -qO- http://127.0.0.1:8428/metrics | grep -E '^vm_(rows|data_size_bytes)\\{type=\"(storage|indexdb)/' | grep -v ' 0\$'"
    printf '%s\tvictoriametrics\t%s\t%s\n' "$s" \
        "$(printf '%s\n' "$CAPTURE_OUT" | awk '/^vm_rows\{type="storage\// { s += $2 } END { print s }')" \
        "$(printf '%s\n' "$CAPTURE_OUT" | awk '/^vm_data_size_bytes/ { s += $2 } END { print s }')" >> "$TSV"

    # --- TimescaleDB ---
    t="b_$s"
    capture "$f" "$s: TimescaleDB, гипертаблица" "$(psql_cmd "CREATE TABLE $t (time timestamptz NOT NULL, series_id int NOT NULL, value double precision NOT NULL); SELECT create_hypertable('$t', by_range('time', INTERVAL '1 day'));")"
    capture "$f" "$s: TimescaleDB, загрузка" "$GEN ts -table $t -shape $s -series 200 -span 24h -step 15s -end $END"
    sql "VACUUM ANALYZE $t" >/dev/null
    SIZE="SELECT (SELECT count(*) FROM $t) AS samples, hypertable_size('$t') AS bytes, round(hypertable_size('$t')::numeric / (SELECT count(*) FROM $t), 2) AS bytes_per_sample"
    capture "$f" "$s: TimescaleDB, без сжатия" "$(psql_cmd "$SIZE")"
    printf '%s\ttimescaledb\t%s\n' "$s" "$(printf '%s\n' "$CAPTURE_OUT" | awk -F'|' 'NR == 3 { gsub(/ /, ""); print $1 "\t" $2 }')" >> "$TSV"
    capture "$f" "$s: TimescaleDB, сжатие" "$(psql_cmd "ALTER TABLE $t SET (timescaledb.compress, timescaledb.compress_segmentby = 'series_id', timescaledb.compress_orderby = 'time'); SELECT count(compress_chunk(c)) AS compressed_chunks FROM show_chunks('$t') c;")"
    capture "$f" "$s: TimescaleDB, после сжатия" "$(psql_cmd "$SIZE")"
    printf '%s\ttimescaledb-compressed\t%s\n' "$s" "$(printf '%s\n' "$CAPTURE_OUT" | awk -F'|' 'NR == 3 { gsub(/ /, ""); print $1 "\t" $2 }')" >> "$TSV"
done
python3 scripts/report.py
log "записано: $FIXTURES_DIR/$f.txt, $TSV, $FIXTURES_DIR/bytes.md"
