#!/usr/bin/env bash
#
# 05-query.sh — один счётчик, один запрос, две системы.
#
# Час истории одного счётчика (шаг 15 с, приращения — случайные целые 0..19)
# уходит по remote_write и в Prometheus, и в VictoriaMetrics. Затем один и тот
# же PromQL-запрос выполняется в обеих в одной точке времени T, не совпадающей
# с моментами точек. Правильный ответ для increase() считается вручную: значение
# счётчика в T минус значение в T-5m.
#
#   bash scripts/05-query.sh   → fixtures/05-query.txt
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=05-query
fixture_header "$f"
fresh prom vm
wait_http tsdb-prom http://127.0.0.1:9090/-/ready
wait_http tsdb-vm http://127.0.0.1:8428/health

END_S=$(( $(date -u +%s) / 3600 * 3600 ))
END="$(date -u -r "$END_S" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d "@$END_S" '+%Y-%m-%dT%H:%M:%SZ')"
T=$(( END_S - 600 + 7 ))     # за 10 минут до конца, в 7 с после точки

for target in "prom:9090/api/v1/write" "vm:8428/api/v1/write"; do
    capture "$f" "запись: час истории счётчика → ${target%%:*}" \
        "$GEN rw -url http://$target -shape counter -series 1 -span 1h -step 15s -end $END"
done
sleep 5   # VictoriaMetrics делает записанное видимым поиску не сразу

for srv in http://127.0.0.1:9090 http://vm:8428; do
    for q in 'demo_counter_total' 'demo_counter_total offset 5m' 'increase(demo_counter_total[5m])' 'rate(demo_counter_total[5m])'; do
        capture "$f" "$q → $srv" "docker exec tsdb-prom promtool query instant --time=$T $srv '$q'"
    done
done
capture "$f" "точки в окне (T-5m, T] → Prometheus" "docker exec tsdb-prom promtool query instant --time=$T http://127.0.0.1:9090 'demo_counter_total[5m]'"
log "записано: $FIXTURES_DIR/$f.txt"
