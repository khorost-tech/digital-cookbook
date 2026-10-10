#!/usr/bin/env bash
#
# 07-precision.sh — что хранилище возвращает обратно.
#
# Сжатие из 03 сравнивает размер, но не то, что сохранено. Здесь 200 рядов ×
# 50 минут × шаг 15 с каждой формы уходят по remote_write в Prometheus и
# VictoriaMetrics, затем читаются запросом demo_<форма>[3000s] и сравниваются
# с исходными значениями генератора точка в точку.
#
#   bash scripts/07-precision.sh   → fixtures/07-precision.txt
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=07-precision
fixture_header "$f"
fresh prom vm
wait_http tsdb-prom http://127.0.0.1:9090/-/ready
wait_http tsdb-vm http://127.0.0.1:8428/health

# Конец — начало прошлой минуты: VictoriaMetrics по умолчанию не отдаёт
# последние 30 с (-search.latencyOffset).
END_S=$(( $(date -u +%s) / 60 * 60 - 60 ))
END="$(date -u -r "$END_S" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d "@$END_S" '+%Y-%m-%dT%H:%M:%SZ')"
SHAPES="${SHAPES:-gauge gauge2 counter}"

for s in $SHAPES; do
    for target in "prom:9090/api/v1/write" "vm:8428/api/v1/write"; do
        capture "$f" "$s: запись → ${target%%:*}" \
            "$GEN rw -url http://$target -shape $s -series 200 -span 50m -step 15s -end $END"
    done
done
docker exec tsdb-vm wget -qO- http://127.0.0.1:8428/internal/force_flush >/dev/null
sleep 5
for s in $SHAPES; do
    for srv in prom:9090 vm:8428; do
        capture "$f" "$s: чтение и сверка ← ${srv%%:*}" \
            "$GEN check -url http://$srv -shape $s -series 200 -span 50m -step 15s -end $END"
    done
done
log "записано: $FIXTURES_DIR/$f.txt"
