#!/usr/bin/env bash
#
# probe.sh <движок> — фактические версия, состав кластера и уровень изоляции
# по умолчанию. Всё, что статья говорит о версиях, берётся отсюда.
#
#   bash scripts/probe.sh crdb    → fixtures/00-probe-crdb.txt

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: $ENGINES"
require_running "$e"
f="00-probe-$e"
fixture_header "$f" "$e"

capture "$f" "образы" "docker inspect -f '{{.Name}} {{.Config.Image}}' $(members "$e")"

case "$e" in
crdb)
    capture "$f" "версия" "$(sql_cmd crdb 'SELECT version();')"
    capture "$f" "узлы" "docker exec crdb1 cockroach node status --insecure --host=localhost:26257 --format=table"
    capture "$f" "изоляция по умолчанию" "$(sql_cmd crdb 'SHOW default_transaction_isolation;')"
    capture "$f" "фактор репликации по умолчанию" "$(sql_cmd crdb 'SHOW ZONE CONFIGURATION FROM RANGE default;')"
    ;;
yb)
    capture "$f" "версия" "$(sql_cmd yb 'SELECT version();')"
    capture "$f" "tablet-серверы" "docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100 list_all_tablet_servers"
    capture "$f" "мастера" "docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100 list_all_masters"
    capture "$f" "фактор репликации" "docker exec yb1 bin/yb-admin --master_addresses yb1:7100,yb2:7100,yb3:7100 get_universe_config"
    capture "$f" "изоляция по умолчанию" "$(sql_cmd yb 'SHOW default_transaction_isolation;')"
    capture "$f" "READ COMMITTED включён?" "$(sql_cmd yb 'SHOW yb_effective_transaction_isolation_level;')"
    ;;
tidb)
    capture "$f" "версия" "$(sql_cmd tidb 'SELECT tidb_version()\G')"
    capture "$f" "TiKV-хранилища" "$(sql_cmd tidb 'SELECT store_id, address, store_state_name, leader_count, region_count, version FROM information_schema.tikv_store_status ORDER BY store_id;')"
    capture "$f" "изоляция и режим транзакций" "$(sql_cmd tidb 'SELECT @@transaction_isolation, @@tidb_txn_mode;')"
    capture "$f" "фактор репликации" "$(sql_cmd tidb "SHOW CONFIG WHERE name = 'replication.max-replicas';")"
    ;;
ob)
    capture "$f" "версия" "$(sql_cmd ob 'SELECT version(); SELECT @@version_comment;')"
    capture "$f" "observer-ы" "docker exec ob obclient -h127.0.0.1 -P2881 -uroot@sys -A -t -e 'SELECT svr_ip, zone, status, with_rootserver FROM oceanbase.DBA_OB_SERVERS;'"
    capture "$f" "тенанты" "docker exec ob obclient -h127.0.0.1 -P2881 -uroot@sys -A -t -e 'SELECT tenant_name, tenant_type, compatibility_mode, primary_zone, locality FROM oceanbase.DBA_OB_TENANTS;'"
    capture "$f" "изоляция по умолчанию" "$(sql_cmd ob 'SELECT @@transaction_isolation;')"
    ;;
pg)
    capture "$f" "версия" "$(sql_cmd pg 'SELECT version();')"
    capture "$f" "изоляция и synchronous_commit" "$(sql_cmd pg 'SHOW default_transaction_isolation; SHOW synchronous_commit;')"
    ;;
mysql)
    capture "$f" "версия и изоляция" "$(sql_cmd mysql 'SELECT version(), @@transaction_isolation;')"
    ;;
esac
log "записано: $FIXTURES_DIR/$f.txt"
