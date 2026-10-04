#!/usr/bin/env bash
# Три режима пулинга и их семантика (через PgBouncer, одна БД под тремя pool_mode):
#   session     — server-соединение закреплено за клиентом на всю сессию. Всё работает,
#                 но мультиплексирование минимально (idle-клиент держит соединение).
#   transaction — server занят на транзакцию, между транзакциями возвращается в пул.
#                 Боевой режим. Многооператорная транзакция работает.
#   statement   — server возвращается после КАЖДОГО оператора. Максимум мультиплексирования,
#                 но транзакция из нескольких операторов невозможна.
# Команды подаём РАЗДЕЛЬНЫМИ запросами (через stdin), иначе psql -c шлёт их одним пакетом
# и statement-режиму нечего ловить.
# Запуск: ./pool-modes.sh
set -uo pipefail
cd "$(dirname "$0")"

pb() {   # $1=dbname(режим); SQL — со stdin, по одному оператору на строку
  docker compose exec -T postgres bash -c \
    "PGPASSWORD=pooldemo psql -h pgbouncer -p 6432 -U postgres -d $1 -v ON_ERROR_STOP=0 -f -" 2>&1
}

TX=$'BEGIN;\nSELECT 1 AS a;\nSELECT 2 AS b;\nCOMMIT;\n'

echo "### transaction — многооператорная транзакция РАБОТАЕТ"
printf '%s' "$TX" | pb opsdemo

echo
echo "### statement — та же транзакция ЛОМАЕТСЯ (server уходит после каждого оператора)"
printf '%s' "$TX" | pb opsdemo_statement

echo
echo "### session — работает как обычное соединение"
printf '%s' "$TX" | pb opsdemo_session

echo
echo "### режим пулинга по каждой БД (SHOW DATABASES, вертикально)"
docker compose exec -T postgres bash -c \
  "PGPASSWORD=pooldemo psql -h pgbouncer -p 6432 -U postgres -d pgbouncer -x -c 'SHOW DATABASES'" 2>&1 \
  | grep -E 'name|pool_mode'
