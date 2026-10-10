#!/usr/bin/env bash
# Где именно ломается «файл бьёт сервер»: рост числа писателей.
# Второй сценарий (первый — латентность в run-latency.sh): здесь не режим
# долговечности, а конкуренция писателей за один файл. У SQLite ровно один
# писатель одновременно; у PostgreSQL — MVCC, конкуренция не блокирует запись.
#
# Пары СОГЛАСОВАНЫ по гарантии долговечности — иначе сравнение измеряло бы
# разницу в обещании (fsync на каждый commit vs без него), а не в архитектуре
# блокировок:
#   ослабленная: sqlite -sync NORMAL  vs pg-tcp -pg-sync off
#   полная:      sqlite -sync FULL    vs pg-tcp -pg-sync on
set -euo pipefail

# Windows/Git Bash: без этого docker compose иногда перегоняет POSIX-подобные
# пути в аргументах через путепреобразование MSYS.
export MSYS_NO_PATHCONV=1

N="${N:-20000}"
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUT:-$SELF_DIR/../fixtures/writers.txt}"

run() { docker compose exec -T bench bench -arm "$1" -op insert -n "$N" -writers "$2" "${@:3}"; }

{
  echo "== конкурентная запись, всего $N вставок на прогон =="
  date -u +"прогон: %Y-%m-%dT%H:%M:%SZ"
  for w in 1 2 4 8 16; do
    echo "-- писателей: $w, ослабленная пара (sqlite NORMAL / pg off) --"
    run sqlite "$w" -sync NORMAL
    run pg-tcp "$w" -pg-sync off
    echo "-- писателей: $w, полная пара (sqlite FULL / pg on) --"
    run sqlite "$w" -sync FULL
    run pg-tcp "$w" -pg-sync on
  done

  echo "== контрольная проверка: busy_timeout не артефакт ================"
  echo "-- writers=8, -busy-timeout 5000 (по умолчанию, для сравнения) --"
  run sqlite 8 -sync NORMAL -busy-timeout 5000
  echo "-- writers=8, -busy-timeout 0 (BusyErrors обязан вырасти) --"
  run sqlite 8 -sync NORMAL -busy-timeout 0
} | tee "$OUT"

echo "фикстура записана: $OUT"
