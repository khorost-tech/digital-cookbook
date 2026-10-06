#!/usr/bin/env bash
# vdbe-probe.sh — отдельная, долгая проба к шагу 8 plans.sh: насколько далеко
# соединению по category до успеха, если поднять и лимит опкодов VDBE.
# Узкий фильтр a.id < 10, лимит переноса поднят, опкоды — до 2 000 000 000.
# Ожидаемый исход на этом стенде — отказ по опкодам даже на таком лимите.
# В plans.sh не входит: один прогон занимает десятки секунд.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"
exec > >(tee "$DIR/vdbe-probe.txt") 2>&1

Q="SELECT count(*) FROM products a JOIN products b ON a.category = b.category WHERE a.id < 10"
O="OPTION (SQL_MOTION_ROW_MAX = 200000, SQL_VDBE_OPCODE_MAX = 2000000000)"
echo "=== JOIN по category, a.id < 10, перенос до 200000, опкоды до 2000000000 ==="
echo "$Q $O"
t0=$(date +%s%N)
out="$(printf '%s\n' "$Q $O" | MSYS_NO_PATHCONV=1 docker exec -i picodata-origin \
    psql "$PICO_DSN" -tA -v ON_ERROR_STOP=1 -f - 2>&1)"; rc=$?
ms=$(( ($(date +%s%N) - t0) / 1000000 ))
echo "$out"
echo "код завершения: $rc, время: $ms мс"
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'max executed vdbe opcodes'; then
    echo "ok: отказ по опкодам и на лимите 2000000000"
    exit 0
fi
echo "!!! исход другой, чем описан в статье — перепроверьте текст" >&2
exit 1
