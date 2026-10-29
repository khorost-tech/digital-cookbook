#!/usr/bin/env bash
#
# 08-manual-replay.sh — ручной откат чекпоинта назад и guard по версии.
#
# Курсор проектора — пара (xid, global_pos). Сброс одного global_pos = 0
# историю целиком НЕ перечитывает: фильтр (xid > cur OR (xid = cur AND
# global_pos > pos)) вернёт только события последней обработанной транзакции
# и не тронет более ранние. Перечитать всё — значит сбросить обе компоненты.
#
# Проверяется на одной истории оба варианта:
#   A — UPDATE checkpoints SET global_pos = 0          (как в старых инструкциях)
#   B — UPDATE checkpoints SET global_pos = 0, xid = 0 (весь курсор)
# В обоих повтор обязан пройти через guard version = $2 - 1 без изменения
# баланса; в B перечитано обязано быть ровно столько событий, сколько в сторе.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/08-manual-replay.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-08
mkdir -p "$OUT"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

psql_c -c "TRUNCATE events, balances, checkpoints RESTART IDENTITY;" >/dev/null

echo "=== Наполняем историю и проецируем её целиком ==="
"$OUT/loadgen" -mode append-fine -workers 16 -streams 64 -duration 10s > "$OUT/fill.json"
"$OUT/loadgen" -mode project -duration 60s > "$OUT/proj-0.json"
total="$(sql_value "SELECT count(*) FROM events;")"
sum0="$(sql_value "SELECT sum(liters) FROM balances;")"
log "событий в сторе: $total; сумма баланса: $sum0"

ops() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ops"])' "$1"; }
skipped() { grep -o 'пропущено как уже применённое: [0-9]*' "$1" | awk '{s+=$NF} END{print s+0}'; }

replay() {   # replay <метка> <SET-часть UPDATE>
    local label="$1" set="$2"
    psql_c -c "UPDATE checkpoints SET $set WHERE projector = 'balances';" >/dev/null
    "$OUT/loadgen" -mode project -duration 60s > "$OUT/replay-$label.json" 2> "$OUT/replay-$label.err"
    local o s sum
    o="$(ops "$OUT/replay-$label.json")"; s="$(skipped "$OUT/replay-$label.err")"
    sum="$(sql_value "SELECT sum(liters) FROM balances;")"
    echo "  вариант $label (SET $set): перечитано $o из $total, пропущено guard'ом $s, сумма $sum" >&2
    echo "$o $s $sum"
}

echo
echo "=== A: сброс только global_pos ==="
read -r opsA skA sumA < <(replay A "global_pos = 0")
echo
echo "=== B: сброс всего курсора (xid, global_pos) ==="
read -r opsB skB sumB < <(replay B "global_pos = 0, xid = '0'::xid8")

echo
[ "$sumA" = "$sum0" ] && [ "$sumB" = "$sum0" ] \
    || fail "баланс изменился после повтора: было $sum0, стало A=$sumA B=$sumB"
[ "$skA" = "$opsA" ] || fail "A: перечитано $opsA, а guard отсёк $skA — часть применена повторно"
[ "$opsB" = "$total" ] || fail "B: перечитано $opsB из $total — сброс всего курсора обязан перечитать историю"
[ "$skB" = "$total" ] || fail "B: guard отсёк $skB из $total"
[ "$opsA" -lt "$total" ] || fail "A: сброс одного global_pos перечитал всю историю — объяснение про составной курсор неверно"
echo "OK: сброс одного global_pos перечитал $opsA из $total; сброс всего курсора — все $total,"
echo "    guard отсёк все повторы, сумма баланса не изменилась ($sum0)."
