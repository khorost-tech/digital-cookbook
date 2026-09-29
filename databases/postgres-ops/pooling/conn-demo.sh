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

# ⚠️ Первая редакция этого замера считала ВСЕ pgbench-бэкенды в базе:
#   WHERE datname='opsdemo' AND application_name LIKE 'pgbench%'
# — без привязки к тому, ЧЕЙ это пул. Пулер после ухода клиентов не закрывает
# server-соединения сразу, а держит их в пуле (idle до server_idle_timeout),
# поэтому каждый следующий замер видел свои ~5 плюс всё, что осталось от
# предыдущих пулеров. Получался ряд 5 -> 10 -> 15, который читался как
# «у каждого своя политика роста пула», хотя был артефактом порядка запуска:
# переставь пулеры местами — и «политика» переедет вместе с порядком.
#
# Теперь замер привязан к источнику соединения и не зависит от порядка:
#   1) пулер перезапускается перед своим замером — пул стартует пустым;
#   2) считаются только бэкенды с client_addr ИМЕННО этого пулера.
# Для контроля печатается и общее число pgbench-бэкендов в базе: расхождение
# между «свои» и «всего» — это и есть остаток от предыдущих замеров, который
# раньше молча приписывался текущему пулеру.
measure() {   # $1=host $2=port $3=label
  if [ "$1" != "postgres" ]; then
    docker compose restart "$1" >/dev/null 2>&1
    sleep 2   # дать пулеру подняться с пустым пулом
  fi

  local ip
  ip=$($DE getent hosts "$1" | awk '{print $1}')
  if [ -z "$ip" ]; then
    printf '%-30s НЕ РАЗРЕШЁН ХОСТ %s — замер пропущен\n' "$3" "$1" >&2
    return 1
  fi

  $DE bash -c "PGPASSWORD=pooldemo pgbench -h $1 -p $2 -U postgres -d opsdemo -f /tmp/probe.sql -c 50 -j 8 -T 4 -n" >/dev/null 2>&1 &
  local bp=$!
  sleep 2   # дать нагрузке выйти на плато
  local backends total
  backends=$($DE psql -U postgres -d opsdemo -tAc \
    "SELECT count(*) FROM pg_stat_activity WHERE datname='opsdemo' AND backend_type='client backend' AND application_name LIKE 'pgbench%' AND client_addr = '$ip'::inet")
  total=$($DE psql -U postgres -d opsdemo -tAc \
    "SELECT count(*) FROM pg_stat_activity WHERE datname='opsdemo' AND backend_type='client backend' AND application_name LIKE 'pgbench%'")
  wait "$bp"
  printf '%-30s server-бэкендов при 50 клиентах: %-3s (всего pgbench-бэкендов в базе: %s)\n' "$3" "$backends" "$total"
}

echo "50 клиентов на каждый endpoint; считаем реальные соединения к postgres:"
measure postgres  5432 "напрямую (без пулера)"
measure pgbouncer 6432 "через PgBouncer (pool=5)"
measure pgcat     6433 "через pgcat (pool=5)"
measure odyssey   6434 "через Odyssey (pool=5)"
