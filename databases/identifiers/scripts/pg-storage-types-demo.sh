#!/usr/bin/env bash
# Артефакт 6: как хранить UUID в PostgreSQL — uuid (16 байт нативно) против
# bytea (16 байт, но без специализированного типа/операторов) против text
# (36 символов + varlena-заголовок). Значения того же типа и распределения
# (независимые uuidv7(), PG 18 встроенная функция, каждая таблица наполнена
# своим вызовом generate_series -> uuidv7() — сами значения РАЗНЫЕ),
# различие между таблицами — тип колонки. Плюс демонстрация из
# db-indexes: сравнение uuid-колонки с приведённым к text значением
# (::text-каст) НЕ покрывается индексом по uuid — частая ошибка ORM/ручных
# запросов, которая незаметно превращает Index Scan в Seq Scan.
#
# Гипотеза как исполняемая проверка (сигнал засчитан, только если оба верны):
#   total_bytes(s_text) > total_bytes(s_uuid)             -- text крупнее uuid
#   AND план для id::text = <text> НЕ является Index-скан по id
#       (т.е. каст действительно ломает покрытие индекса)
# Иначе скрипт печатает "DEMO FAILED: ..." и выходит с кодом 1. Недоступный
# PG тоже приводит к ненулевому коду (set -e на упавшей команде psql/docker
# compose exec). Числа печатаются как есть, без подгонки.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
N=${N:-200000}
PSQL=(docker compose -f compose/compose.yml exec -T pg psql -U ids -d ids -tAX)

"${PSQL[@]}" -c "
  drop table if exists s_uuid, s_bytea, s_text;
  create table s_uuid (id uuid primary key);
  create table s_bytea(id bytea primary key);
  create table s_text (id text primary key);
  insert into s_uuid  select uuidv7() from generate_series(1,$N);
  insert into s_bytea select uuid_send(uuidv7()) from generate_series(1,$N);
  insert into s_text  select uuidv7()::text from generate_series(1,$N);
  analyze s_uuid, s_bytea, s_text;
" >/dev/null

declare -A BYTES
echo "--- pg_total_relation_size (таблица + первичный индекс + toast) ---"
for t in s_uuid s_bytea s_text; do
  b=$("${PSQL[@]}" -c "select pg_total_relation_size('$t');")
  BYTES[$t]="$b"
  printf "%-8s total_bytes=%s (%s)\n" "$t" "$b" \
    "$("${PSQL[@]}" -c "select pg_size_pretty(pg_total_relation_size('$t'));")"
done

# Покрытие индекса: сравнение uuid-колонки со строкой (после ::text-каста)
# рвёт применимость btree-индекса по uuid, потому что каст делает id
# вычисляемым выражением, а не голой колонкой -- планировщик больше не
# может использовать индекс id_pkey для равенства по нему напрямую.
PROBE=$("${PSQL[@]}" -c "select id from s_uuid limit 1;")

echo "--- план: WHERE id = <uuid> (ожидание: индекс покрывает) ---"
PLAN1=$("${PSQL[@]}" -c "explain (costs off) select 1 from s_uuid where id = '$PROBE'::uuid;")
echo "$PLAN1"

echo "--- план: WHERE id::text = <text> (ожидание: каст ломает покрытие) ---"
PLAN2=$("${PSQL[@]}" -c "explain (costs off) select 1 from s_uuid where id::text = '$PROBE';")
echo "$PLAN2"

# Фальсификация.
SIZE_OK=false
if [ "${BYTES[s_text]}" -gt "${BYTES[s_uuid]}" ]; then SIZE_OK=true; fi

PLAN1_INDEX=false
if echo "$PLAN1" | grep -qE "Index( Only)? Scan"; then PLAN1_INDEX=true; fi

PLAN2_INDEX=false
if echo "$PLAN2" | grep -qE "Index( Only)? Scan"; then PLAN2_INDEX=true; fi

if [ "$SIZE_OK" = true ] && [ "$PLAN1_INDEX" = true ] && [ "$PLAN2_INDEX" = false ]; then
  echo "SIGNAL: s_text крупнее s_uuid (${BYTES[s_text]} > ${BYTES[s_uuid]}), прямое сравнение id использует индекс, ::text-каст ломает его покрытие (Seq Scan)"
  exit 0
else
  echo "DEMO FAILED: не все условия выполнены (size_ok=$SIZE_OK plan1_index=$PLAN1_INDEX plan2_index=$PLAN2_INDEX s_uuid=${BYTES[s_uuid]} s_text=${BYTES[s_text]})"
  exit 1
fi
