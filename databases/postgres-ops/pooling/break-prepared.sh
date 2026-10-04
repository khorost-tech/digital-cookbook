#!/usr/bin/env bash
# Server-side prepared statements и transaction pooling.
# Исторически transaction pooling ломал prepared: statement готовится на одном
# server-соединении, а следующий запрос попадает на другое, где его нет.
# Современный PgBouncer (1.21+) решает это, отслеживая prepared и переигрывая их на
# каждый server — по умолчанию max_prepared_statements=200. Показываем ОБЕ стороны.
# Запуск: ./break-prepared.sh
set -uo pipefail
cd "$(dirname "$0")"

admin() { docker compose exec -T postgres bash -c \
  "PGPASSWORD=pooldemo psql -h pgbouncer -p 6432 -U postgres -d pgbouncer -c \"$1\"" >/dev/null 2>&1; }
bench() { docker compose exec -T postgres bash -c \
  "PGPASSWORD=pooldemo pgbench -h pgbouncer -p 6432 -U postgres -d opsdemo -M prepared -c 10 -j 4 -t 50 -S -n" 2>&1; }

echo "### max_prepared_statements=0 — историческое поведение (prepared ЛОМАЮТСЯ)"
admin "SET max_prepared_statements=0"
bench | grep -iE 'does not exist|already exists|aborted' | head -3
echo "  (10 клиентов, prepared-протокол — сыплются ошибки P_0 does not exist / already exists)"

echo
echo "### max_prepared_statements=200 — дефолт PgBouncer 1.25.2 (prepared РАБОТАЮТ)"
admin "SET max_prepared_statements=200"
bench | grep -iE 'processed|tps' | head -2

# оставляем дефолт
admin "SET max_prepared_statements=200"
