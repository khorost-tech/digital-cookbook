#!/usr/bin/env bash
#
# 02-latency.sh <движок> — p50/p95/p99 шести видов операций при разном RTT
# между узлами. Один клиент, одно соединение, операции по очереди: меряется
# цена одной операции, а не пропускная способность.
#
#   read    точечное чтение по ключу (автокоммит)
#   write1  UPDATE одной строки (автокоммит)
#   txn1x2  явная транзакция: две строки в ОДНОМ диапазоне
#   txn2    явная транзакция: две строки в ДВУХ диапазонах
#   txn1x4  явная транзакция: четыре строки в ОДНОМ диапазоне
#   txn4    явная транзакция: четыре строки в ЧЕТЫРЁХ диапазонах
#
# Для кластеров RTT перебирается: 0 (как есть), 2 мс (зоны), 30 мс (регионы).
# Одиночные серверы (pg, mysql, ob) меряются только как есть.
#
#   bash scripts/02-latency.sh crdb           → fixtures/02-latency-crdb.txt
#   RTTS="0 2" N=200 bash scripts/02-latency.sh yb

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: $ENGINES"
require_running "$e"
N="${N:-300}"
f="02-latency-$e"
fixture_header "$f" "$e"

case "$e" in
    crdb|yb|tidb) rtts="${RTTS:-0 2 30}" ;;
    *) rtts=0 ;;
esac

# Таблицу и раскладку лидеров готовит 01: так фикстура 01 всегда описывает
# ровно то состояние, на котором снят этот замер.
case "$e" in
    crdb|yb|tidb|ob) bash scripts/01-distribution.sh "$e" >/dev/null ;;
    *) bash -c "$(bench_cmd latency -engine "$e" -setup -n 0)" >/dev/null ;;
esac

for rtt in $rtts; do
    if [ "$e" = crdb ] || [ "$e" = yb ] || [ "$e" = tidb ]; then
        capture "$f" "RTT между узлами: ${rtt} мс" "bash scripts/netem.sh $e $rtt"
    fi
    capture "$f" "замер при RTT ${rtt} мс" "$(bench_cmd latency -engine "$e" -n "$N")"
done

if [ "$e" = crdb ] || [ "$e" = yb ] || [ "$e" = tidb ]; then
    # Ребалансировка может увести лидера с назначенного узла посреди прогона.
    # Снимок после замера показывает, жила ли раскладка из 01 до конца.
    capture "$f" "лидеры после замера" "$(leaders_cmd "$e")"
    bash scripts/netem.sh "$e" 0 >/dev/null 2>&1 || true
    log "задержка снята"
fi
log "записано: $FIXTURES_DIR/$f.txt"
