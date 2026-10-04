#!/usr/bin/env bash
# Артефакт 1: как тип ключа влияет на локальность вставки в B-tree.
# Три ветки (bigint / uuid4 / uuid7) различаются РОВНО типом ключа: та же
# схема, та же полезная нагрузка, то же N. Меряем СТРУКТУРНЫЕ величины
# (размеры, bloat, WAL-байты) — они воспроизводимы и не зависят от нагрузки
# на хост, в отличие от секундомера. Все узлы на одном хосте: показываем
# ХАРАКТЕР эффекта (у случайного ключа хуже локальность), не его величину.
#
# Гипотеза как исполняемая проверка (сигнал засчитан, только если оба верны):
#   idx_leaf_density(uuid4) < idx_leaf_density(uuid7)   -- рыхлее листья
#   AND wal_bytes(uuid4)    > wal_bytes(bigint)         -- больше WAL за вставку
# Иначе скрипт печатает "DEMO FAILED: ..." и выходит с кодом 1. Недоступный
# PG/отсутствующая схема тоже приводят к ненулевому коду (set -e на любой
# упавшей команде psql/go), без молчаливого пропуска.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
N=${N:-500000}
: "${GOPROXY:=https://go.khorost.tech,direct}"
export GOPROXY
PSQL=(docker compose -f compose/compose.yml exec -T pg psql -U ids -d ids -tAX)

measure() { # $1=table -> "table_bytes idx_bytes leaf_density"
  "${PSQL[@]}" -c "select pg_relation_size('$1'), pg_relation_size((select indexrelid from pg_index where indrelid='$1'::regclass and indisprimary)), (select avg_leaf_density from pgstatindex((select indexrelid from pg_index where indrelid='$1'::regclass and indisprimary)::regclass::text));"
}
wal_lsn() { "${PSQL[@]}" -c "select pg_current_wal_lsn();"; }

declare -A REL IDX DENS WAL
for k in bigint uuid4 uuid7; do
  table="k_$k"
  "${PSQL[@]}" -c "truncate $table;" >/dev/null
  before=$(wal_lsn)
  # loader — Go-модуль в loader/ (Task 2). Вызываем хостовым `go run .`:
  # в этом окружении Go стоит на хосте и доступен из bash напрямую (PATH),
  # WSL-обёртка не нужна — `cd loader && go run .` работает как есть.
  ( cd loader && go run . -key="$k" -n="$N" ) 2>&1 | tee /dev/stderr | grep -q "rate=" || { echo "DEMO FAILED: loader не отчитался для $k"; exit 1; }
  after=$(wal_lsn)
  WAL[$k]=$("${PSQL[@]}" -c "select pg_wal_lsn_diff('$after','$before');")
  read -r REL[$k] IDX[$k] DENS[$k] < <(measure "$table" | tr '|' ' ')
done

printf "%-8s %14s %14s %12s %14s\n" key table_bytes idx_bytes leaf_density wal_bytes
for k in bigint uuid4 uuid7; do
  printf "%-8s %14s %14s %12s %14s\n" "$k" "${REL[$k]}" "${IDX[$k]}" "${DENS[$k]}" "${WAL[$k]}"
done

# Фальсификация: у uuid4 индекс должен быть крупнее/рыхлее, чем у uuid7,
# и WAL больше, чем у bigint. leaf_density — процент заполнения листьев:
# у случайного ключа он НИЖЕ (page splits оставляют полупустые страницы).
awk -v d4="${DENS[uuid4]}" -v d7="${DENS[uuid7]}" -v w4="${WAL[uuid4]}" -v wb="${WAL[bigint]}" 'BEGIN{
  if (d4+0 < d7+0 && w4+0 > wb+0) { print "SIGNAL: uuid4 хуже локальность (leaf_density ниже) и WAL больше bigint"; exit 0 }
  else { print "DEMO FAILED: знак эффекта не тот (d4="d4" d7="d7" w4="w4" wb="wb")"; exit 1 }
}'
