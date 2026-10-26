#!/usr/bin/env bash
#
# 02-design.sh — артефакт к статье «Границы агрегата и гранулярность событий».
#
# Предмет замера: цена крупного агрегата. Один стрим на всю базу против
# стрима на резервуар при одинаковой нагрузке. Различается РОВНО число
# стримов — команда, домен, схема и код одни и те же.
#
# Порядок вариантов чередуется по раундам: замер в фиксированном порядке
# не отличает эффект границ от дрейфа машины.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/02-design.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-02
mkdir -p "$OUT"

log "собираем loadgen…"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

jget() { # $1 = файл, $2 = ключ
    python3 -c "import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])" "$1" "$2"
}

reset_events() {
    psql_c -c "TRUNCATE events, snapshots, balances, checkpoints;" >/dev/null
}

run_one() { # $1 = метка (coarse|fine), $2 = число стримов, $3 = раунд
    local label="$1"
    local streams="$2"
    local r="$3"
    local f="$OUT/$label-r$r.json"
    reset_events
    "$OUT/loadgen" -workers "$WORKERS" -streams "$streams" -duration "$DUR" \
        -mode "append-$label" > "$f"
    printf '    %-7s ops=%-7s конфликтов=%-7s p50=%-8s p95=%s\n' \
        "$label" "$(jget "$f" ops)" "$(jget "$f" conflicts)" \
        "$(jget "$f" p50_ms)" "$(jget "$f" p95_ms)"
}

WORKERS="${WORKERS:-16}"
DUR="${DUR:-30s}"
ROUNDS="${ROUNDS:-3}"

echo "=== Цена крупного агрегата: один стрим против многих ==="
log "воркеров $WORKERS, по $DUR на вариант, раундов $ROUNDS"

for r in $(seq 1 "$ROUNDS"); do
    echo "  --- раунд $r:"
    if [ $(( r % 2 )) -eq 1 ]; then
        run_one coarse 1  "$r"; run_one fine 64 "$r"
    else
        run_one fine 64 "$r"; run_one coarse 1  "$r"
    fi
done

echo "  --- сводка (ops / конфликтов):"
for r in $(seq 1 "$ROUNDS"); do
    printf '    раунд %s: coarse=%s/%s  fine=%s/%s\n' "$r" \
        "$(jget "$OUT/coarse-r$r.json" ops)" "$(jget "$OUT/coarse-r$r.json" conflicts)" \
        "$(jget "$OUT/fine-r$r.json" ops)"   "$(jget "$OUT/fine-r$r.json" conflicts)"
done

echo
echo "=== Постусловие ==="
coarse_conf="$(jget "$OUT/coarse-r1.json" conflicts)"
[ "$coarse_conf" -gt 0 ] \
    || fail "у крупного агрегата ноль конфликтов — конкуренция не дошла, замер несостоятелен"
log "конфликты у крупного агрегата есть ($coarse_conf в первом раунде) — замер состоятелен"

log "ГРАНИЦА ВЫВОДА. Измерена цена конкуренции за версию при разном числе"
log "стримов. Замер НЕ отвечает на вопрос, где проводить границу агрегата:"
log "мелкие стримы снимают конфликты, но инвариант «суммарный запас базы»"
log "перестаёт держаться транзакционно, и это не измеряется вовсе."
