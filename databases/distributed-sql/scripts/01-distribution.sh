#!/usr/bin/env bash
#
# 01-distribution.sh <движок> — таблица kv из 4000 строк, разрезанная на четыре
# диапазона по границам 1e6/2e6/3e6. Показывает, как движок называет единицу
# распределения, где её реплики и где лидер.
#
#   bash scripts/01-distribution.sh crdb   → fixtures/01-distribution-crdb.txt

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: $ENGINES"
require_running "$e"
f="01-distribution-$e"
fixture_header "$f" "$e"

capture "$f" "создание и предразбиение kv" "$(bench_cmd latency -engine "$e" -setup -n 0)"

# Сплит и SCATTER асинхронны: даём перераспределению осесть.
case "$e" in crdb|yb|tidb) log "ждём 20 с, пока осядет перераспределение"; sleep 20 ;; esac

# Лидеров разносим явно и НЕ на шлюз — см. scripts/place-leaders.sh.
case "$e" in
crdb|yb|tidb)
    capture "$f" "перенос лидеров: диапазоны 0 и 2 → узел 2, 1 и 3 → узел 3" "bash scripts/place-leaders.sh $e"
    sleep 5 ;;
esac

case "$e" in
crdb)
    capture "$f" "диапазоны kv: реплики и leaseholder" \
        "$(sql_cmd crdb 'SELECT range_id, start_key, end_key, lease_holder, replicas FROM [SHOW RANGES FROM TABLE kv WITH DETAILS] ORDER BY start_key;')"
    capture "$f" "узел → контейнер" \
        "docker exec crdb1 cockroach node status --insecure --host=localhost:26257 --format=tsv | cut -f1,2"
    capture "$f" "всего диапазонов в кластере" \
        "$(sql_cmd crdb 'SELECT count(*) AS ranges FROM [SHOW CLUSTER RANGES];')"
    ;;
yb)
    capture "$f" "таблетки kv: границы и лидер" \
        "docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100 list_tablets ysql.yugabyte kv"
    capture "$f" "границы разбиения" \
        "$(sql_cmd yb "SELECT yb_get_range_split_clause('kv'::regclass);")"
    ;;
tidb)
    capture "$f" "регионы kv: лидер и пиры" \
        "$(sql_cmd tidb 'SHOW TABLE kv REGIONS;')"
    capture "$f" "store_id → адрес" \
        "$(sql_cmd tidb 'SELECT store_id, address, leader_count, region_count FROM information_schema.tikv_store_status ORDER BY store_id;')"
    ;;
ob)
    capture "$f" "партиции kv: таблетки, лог-стримы, роль реплики" \
        "$(sql_cmd ob "SELECT partition_name, tablet_id, ls_id, svr_ip, role FROM oceanbase.DBA_OB_TABLE_LOCATIONS WHERE table_name = 'kv' ORDER BY partition_name;")"
    ;;
*) fail "$e — одиночный сервер, распределять нечего" ;;
esac
log "записано: $FIXTURES_DIR/$f.txt"
