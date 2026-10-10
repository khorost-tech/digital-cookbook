#!/usr/bin/env bash
#
# 04-cardinality.sh — цена ряда: память Prometheus и VictoriaMetrics при росте
# числа активных рядов.
#
# Метрика demo_requests с лейблом user_id — типичная ошибка, когда в лейбл
# попадает идентификатор. Ступени: 10 тыс., 100 тыс., 300 тыс., 1 млн рядов, у каждого
# одна свежая точка по remote_write. После каждой ступени — пауза и снимок
# собственных метрик процесса: RSS, занятая куча Go, число рядов.
#
#   bash scripts/04-cardinality.sh   → fixtures/04-cardinality.txt, fixtures/cardinality.tsv
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=04-cardinality
fixture_header "$f"
TSV="$FIXTURES_DIR/cardinality.tsv"
printf 'system\tseries\trss_bytes\theap_inuse_bytes\n' > "$TSV"
SETTLE="${SETTLE:-30}"

fresh prom vm
wait_http tsdb-prom http://127.0.0.1:9090/-/ready
wait_http tsdb-vm http://127.0.0.1:8428/health

PROM_M="docker exec tsdb-prom wget -qO- http://127.0.0.1:9090/metrics | grep -E '^(prometheus_tsdb_head_series|process_resident_memory_bytes|go_memstats_heap_inuse_bytes) '"
VM_M="docker exec tsdb-vm wget -qO- http://127.0.0.1:8428/metrics | grep -E '^(vm_new_timeseries_created_total|process_resident_memory_bytes|go_memstats_heap_inuse_bytes) '"

snap() {  # <ступень>
    sleep "$SETTLE"
    capture "$f" "Prometheus: $1 рядов" "$PROM_M"
    printf 'prometheus\t%s\t%s\t%s\n' "$1" \
        "$(printf '%s\n' "$CAPTURE_OUT" | awk '$1 == "process_resident_memory_bytes" { printf "%.0f", $2 }')" \
        "$(printf '%s\n' "$CAPTURE_OUT" | awk '$1 == "go_memstats_heap_inuse_bytes" { printf "%.0f", $2 }')" >> "$TSV"
    capture "$f" "VictoriaMetrics: $1 рядов" "$VM_M"
    printf 'victoriametrics\t%s\t%s\t%s\n' "$1" \
        "$(printf '%s\n' "$CAPTURE_OUT" | awk '$1 == "process_resident_memory_bytes" { printf "%.0f", $2 }')" \
        "$(printf '%s\n' "$CAPTURE_OUT" | awk '$1 == "go_memstats_heap_inuse_bytes" { printf "%.0f", $2 }')" >> "$TSV"
}

snap 0
prev=0
for total in 10000 100000 300000 1000000; do
    for target in "prom:9090/api/v1/write" "vm:8428/api/v1/write"; do
        capture "$f" "запись: ряды $prev..$((total - 1)) → ${target%%:*}" \
            "$GEN card -url http://$target -series $((total - prev)) -offset $prev"
    done
    prev=$total
    snap "$total"
done
# Запрос, который трогает все ряды метрики, — в обе системы одной командой.
for srv in http://127.0.0.1:9090 http://vm:8428; do
    capture "$f" "count(demo_requests) → $srv" "docker exec tsdb-prom promtool query instant $srv 'count(demo_requests)'"
done
column -t "$TSV" >&2
log "записано: $FIXTURES_DIR/$f.txt, $TSV"
