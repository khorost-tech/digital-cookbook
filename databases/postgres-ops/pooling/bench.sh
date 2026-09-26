#!/usr/bin/env bash
# Бенчмарк: где пулер реально помогает. Два сценария, оба — read-only (-S).
#   A. Перегрузка: клиентов больше, чем max_connections. Напрямую база отказывает,
#      через пулер клиенты мультиплексируются и работают.
#   B. Переподключение (-C): каждая транзакция открывает новое соединение. Напрямую это
#      форк процесса postgres на каждую; пулер переиспользует горстку server-соединений.
# Устойчивую нагрузку с pool_size=5 намеренно не меряем — там малый пул был бы искусственным
# горлышком и вводил бы в заблуждение. Ценность пулера — в защите и переподключении.
# Запуск: ./bench.sh   (pgbench-таблицы инициализируются автоматически)
set -uo pipefail
cd "$(dirname "$0")"
DE="docker compose exec -T postgres"

$DE bash -c "PGPASSWORD=pooldemo pgbench -h postgres -p 5432 -U postgres -d opsdemo -i -s 2 -q" >/dev/null 2>&1

# tps данного endpoint; аргументы после порта передаются в pgbench
run() {   # $1=host $2=port $3.. = доп. флаги pgbench
  local host=$1 port=$2; shift 2
  $DE bash -c "PGPASSWORD=pooldemo pgbench -h $host -p $port -U postgres -d opsdemo -S -n $* 2>&1" \
    | grep -iE 'tps|too many|could not|connection' | head -2
}

echo "=== A. Перегрузка: 150 клиентов при max_connections=100 ==="
echo "--- напрямую к postgres (ожидаем отказ) ---"
run postgres  5432 -c 150 -j 8 -T 4
echo "--- через PgBouncer ---"
run pgbouncer 6432 -c 150 -j 8 -T 4
echo "--- через pgcat ---"
run pgcat     6433 -c 150 -j 8 -T 4
echo "--- через Odyssey ---"
run odyssey   6434 -c 150 -j 8 -T 4

echo
echo "=== B. Переподключение (-C): новое соединение на каждую транзакцию ==="
echo "--- напрямую к postgres (форк процесса на каждую) ---"
run postgres  5432 -C -c 20 -j 8 -T 4
echo "--- через PgBouncer (переиспользование) ---"
run pgbouncer 6432 -C -c 20 -j 8 -T 4
echo "--- через pgcat ---"
run pgcat     6433 -C -c 20 -j 8 -T 4
echo "--- через Odyssey ---"
run odyssey   6434 -C -c 20 -j 8 -T 4
