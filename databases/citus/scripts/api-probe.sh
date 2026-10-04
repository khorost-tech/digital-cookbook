#!/usr/bin/env bash
#
# api-probe.sh — фиксирует ФАКТИЧЕСКИЙ API живого Citus 14.1: версия
# расширения, состав узлов, распределение шардов по таблицам и — самое
# важное для следующих задач серии (артефакт 5 / ребаланс) — какие функции
# ребаланса реально существуют в этой версии под этими именами.
#
# Это не формальность: имена функций Citus менялись между мажорными
# версиями (rebalance_table_shards vs citus_rebalance_start, master_add_node
# vs citus_add_node). Если какой-то функции нет — скрипт должен ЭТО
# ПОКАЗАТЬ, а не молчать: планирование следующих артефактов опирается на
# фактический список, а не на предположение.
#
# Требует уже поднятого стенда (bash scripts/up.sh).
#
# Запуск (из databases/citus/):
#   bash scripts/api-probe.sh

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

DB_USER=shard
DB_NAME=shard

log()  { echo "[api-probe] $*" >&2; }
fail() { echo "[api-probe] ОШИБКА: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || fail "docker не найден в PATH."
docker info >/dev/null 2>&1 || fail "docker недоступен (демон не отвечает)."

state="$(docker inspect --format '{{.State.Status}}' citus-coord 2>/dev/null || true)"
[ "$state" = "running" ] || fail "контейнер citus-coord не запущен. Сначала: bash scripts/up.sh"

psql_c() {
  docker exec -i citus-coord psql -X -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"
}

echo "================================================================"
echo " 1. Версия расширения Citus"
echo "================================================================"
psql_c -c "SELECT extname, extversion FROM pg_extension WHERE extname = 'citus';"

extversion="$(psql_c -At -c "SELECT extversion FROM pg_extension WHERE extname = 'citus';")"
[ -n "$extversion" ] || fail "расширение citus не установлено в базе $DB_NAME — pg_extension пуст. Схема не применилась?"
log "extversion = $extversion"

echo
echo "================================================================"
echo " 2. Узлы кластера (pg_dist_node)"
echo "================================================================"
psql_c -c "SELECT nodeid, nodename, nodeport, isactive FROM pg_dist_node ORDER BY nodeid;"

active_nodes="$(psql_c -At -c "SELECT count(*) FROM pg_dist_node WHERE isactive;")"
if [ "$active_nodes" -lt 2 ]; then
  fail "в pg_dist_node меньше 2 активных узлов (нашли: $active_nodes). Воркеры не зарегистрированы — запустите scripts/up.sh."
fi
log "активных узлов: $active_nodes"

echo
echo "================================================================"
echo " 3. Распределение таблиц и число шардов (citus_tables)"
echo "================================================================"
psql_c -c "SELECT table_name, shard_count, colocation_id FROM citus_tables ORDER BY table_name;"

tracked_tables="$(psql_c -At -c "SELECT count(*) FROM citus_tables WHERE table_name::text IN ('customers','orders','shipments','carriers');")"
if [ "$tracked_tables" -ne 4 ]; then
  fail "ожидали 4 таблицы (customers, orders, shipments, carriers) в citus_tables, нашли: $tracked_tables. Схема/распределение не применились — запустите scripts/up.sh."
fi

echo
echo "================================================================"
echo " 4. Функции, нужные артефакту 5 (ребаланс) — ЧТО РЕАЛЬНО ЕСТЬ"
echo "================================================================"
echo "Проверяемый набор: citus_rebalance_start, rebalance_table_shards,"
echo "citus_add_node, citus_drain_node, get_rebalance_progress."
echo "Если функции нет в выводе ниже — её нет в этой версии Citus. Это"
echo "находка для отчёта, а не повод считать, что она 'наверное есть'."
echo

FOUND="$(psql_c -At -c "
SELECT proname FROM pg_proc
 WHERE proname IN ('citus_rebalance_start','rebalance_table_shards',
                    'citus_add_node','citus_drain_node','get_rebalance_progress')
 ORDER BY proname;
")"

psql_c -c "
SELECT proname FROM pg_proc
 WHERE proname IN ('citus_rebalance_start','rebalance_table_shards',
                    'citus_add_node','citus_drain_node','get_rebalance_progress')
 ORDER BY proname;
"

for fn in citus_rebalance_start rebalance_table_shards citus_add_node citus_drain_node get_rebalance_progress; do
  if echo "$FOUND" | grep -qx "$fn"; then
    echo "  [есть]  $fn"
  else
    echo "  [НЕТ]   $fn"
  fi
done

echo
echo "================================================================"
echo " Итог наполнения (для отчёта)"
echo "================================================================"
psql_c -c "
SELECT 'customers' AS t, count(*) FROM customers
UNION ALL SELECT 'orders', count(*) FROM orders
UNION ALL SELECT 'shipments', count(*) FROM shipments
UNION ALL SELECT 'carriers', count(*) FROM carriers;
"

log "Проверка API завершена. extversion=$extversion, активных узлов=$active_nodes."
