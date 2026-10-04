#!/usr/bin/env bash
# Синхронная против асинхронной репликации: цена по latency записи.
#   async (по умолчанию) — commit возвращается после локальной записи WAL. Быстро,
#     но при падении лидера теряются последние неотправленные транзакции (RPO > 0).
#   sync — лидер ждёт подтверждения реплики перед commit. RPO = 0, но каждая запись
#     платит сетевым round-trip до реплики.
# Запуск: ./repl-demo.sh
set -uo pipefail
cd "$(dirname "$0")"
P1="docker compose exec -T patroni1"
PSQL_LEADER="psql host=haproxy,port=5000,user=postgres,password=hapass,dbname=postgres"
DSN="host=haproxy port=5000 user=postgres password=hapass dbname=postgres"

# латентность 2000 одиночных commit-ов (4 клиента) через pgbench
bench_writes() {
  $P1 bash -c "echo \"INSERT INTO ha_demo(val) VALUES('x');\" > /tmp/ins.sql; \
    PGPASSWORD=hapass pgbench -h haproxy -p 5000 -U postgres -d postgres -f /tmp/ins.sql -c 4 -t 500 -n 2>&1" \
    | grep -iE 'latency average|tps'
}

echo "### состояние репликации (по умолчанию async)"
$P1 psql "$DSN" -tAc \
  "SELECT application_name, sync_state FROM pg_stat_replication ORDER BY 1"

echo
echo "### latency записи при АСИНХРОННОЙ репликации"
bench_writes

echo
echo "### включаем синхронную репликацию (Patroni synchronous_mode)"
$P1 patronictl -c /etc/patroni/patroni.yml edit-config --set synchronous_mode=true --force >/dev/null 2>&1
sleep 12   # дать Patroni применить и назначить sync-реплику
$P1 psql "$DSN" -tAc \
  "SELECT application_name, sync_state FROM pg_stat_replication ORDER BY 1"

echo
echo "### latency записи при СИНХРОННОЙ репликации (ждём подтверждения реплики)"
bench_writes

echo
echo "### возвращаем async"
$P1 patronictl -c /etc/patroni/patroni.yml edit-config --set synchronous_mode=false --force >/dev/null 2>&1
