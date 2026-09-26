#!/usr/bin/env bash
# Мультиплексирование: 50 клиентов на каждый endpoint, считаем РЕАЛЬНЫЕ соединения к
# postgres. Напрямую — 50 бэкендов (по процессу на клиента). Через пулер (pool_size=5) —
# около 5: пулер мультиплексирует 50 клиентов на горстку server-соединений.
# Запуск: ./conn-demo.sh  (контейнеры должны быть подняты).
set -uo pipefail
cd "$(dirname "$0")"
DE="docker compose exec -T postgres"

# скрипт нагрузки: держим соединение слегка занятым
$DE bash -c 'echo "SELECT pg_sleep(0.01);" > /tmp/probe.sql'

measure() {   # $1=host $2=port $3=label
  $DE bash -c "PGPASSWORD=pooldemo pgbench -h $1 -p $2 -U postgres -d opsdemo -f /tmp/probe.sql -c 50 -j 8 -T 4 -n" >/dev/null 2>&1 &
  local bp=$!
  sleep 2   # дать нагрузке выйти на плато
  local backends
  backends=$($DE psql -U postgres -d opsdemo -tAc \
    "SELECT count(*) FROM pg_stat_activity WHERE datname='opsdemo' AND backend_type='client backend' AND application_name LIKE 'pgbench%'")
  wait "$bp"
  printf '%-30s server-бэкендов при 50 клиентах: %s\n' "$3" "$backends"
}

echo "50 клиентов на каждый endpoint; считаем реальные соединения к postgres:"
measure postgres  5432 "напрямую (без пулера)"
measure pgbouncer 6432 "через PgBouncer (pool=5)"
measure pgcat     6433 "через pgcat (pool=5)"
measure odyssey   6434 "через Odyssey (pool=5)"
