#!/usr/bin/env bash
#
# workarounds.sh — второй шанс для отказов на CockroachDB: что меняется, если
# сделать то, что посоветовал бы документированный обходной путь. В матрицу не
# идёт — матрица фиксирует поведение «из коробки».
#
#   bash scripts/workarounds.sh   → fixtures/10-workarounds-crdb.txt

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=10-workarounds-crdb
fixture_header "$f"
docker build -q -t smig-alembic-crdb tools/alembic-crdb >/dev/null

newdb() { q crdb defaultdb "DROP DATABASE IF EXISTS $1 CASCADE" >/dev/null; q crdb defaultdb "CREATE DATABASE $1" >/dev/null; }
pq() { echo "docker run --rm --network smig postgres:18.4 psql 'postgres://root@crdb:26257/$1?sslmode=disable' -tAc \"$2\""; }

# 1. Почему ALTER внутри транзакции упирается в блокировку: новые таблицы в
#    CockroachDB 26.2 создаются со schema_locked = true.
newdb t_lock
capture "$f" "DDL новой таблицы: schema_locked по умолчанию" \
    "$(pq t_lock "CREATE TABLE probe (id INT PRIMARY KEY, name TEXT); SHOW CREATE TABLE probe")"
capture "$f" "сессионная переменная, которая это включает" \
    "$(pq t_lock "SHOW create_table_with_schema_locked")"

# 2. Atlas: выключить autocommit_before_ddl параметром подключения.
newdb t_atlas_w
capture "$f" "Atlas: autocommit_before_ddl=off" \
    "docker run --rm --network smig -v \"\$PWD/tools/atlas/migrations:/mig\" arigaio/atlas:1.3.3-community migrate apply --url 'postgres://root@crdb:26257/t_atlas_w?sslmode=disable&search_path=public&options=-c%20autocommit_before_ddl%3Doff' --dir file:///mig"
capture "$f" "Atlas: что осталось в базе" "$(pq t_atlas_w "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY 1")"

# Транзакция на файл — вторая миграция всё равно в отдельной транзакции, и
# ALTER по-прежнему упирается в schema_locked.
newdb t_atlas_w2
capture "$f" "Atlas: autocommit_before_ddl=off и --tx-mode file" \
    "docker run --rm --network smig -v \"\$PWD/tools/atlas/migrations:/mig\" arigaio/atlas:1.3.3-community migrate apply --url 'postgres://root@crdb:26257/t_atlas_w2?sslmode=disable&search_path=public&options=-c%20autocommit_before_ddl%3Doff' --dir file:///mig --tx-mode file"
# Без транзакций — ошибка та же, что по умолчанию: она в служебной части Atlas.
newdb t_atlas_w3
capture "$f" "Atlas: --tx-mode none" \
    "docker run --rm --network smig -v \"\$PWD/tools/atlas/migrations:/mig\" arigaio/atlas:1.3.3-community migrate apply --url 'postgres://root@crdb:26257/t_atlas_w3?sslmode=disable&search_path=public' --dir file:///mig --tx-mode none"

# 3. Alembic: диалект cockroachdb вместо postgresql.
newdb t_alembic_w
A="docker run --rm --network smig -e SMIG_URL='cockroachdb+psycopg://root@crdb:26257/t_alembic_w?sslmode=disable' -v \"\$PWD/tools/alembic:/w\" smig-alembic-crdb"
capture "$f" "Alembic + sqlalchemy-cockroachdb: upgrade head" "$A upgrade head"
capture "$f" "колонки после upgrade" "$(pq t_alembic_w "SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'probe' ORDER BY ordinal_position")"
capture "$f" "Alembic + sqlalchemy-cockroachdb: check (интроспекция)" "$A check"
capture "$f" "Alembic + sqlalchemy-cockroachdb: downgrade -1" "$A downgrade -1"
capture "$f" "колонки после downgrade" "$(pq t_alembic_w "SELECT column_name FROM information_schema.columns WHERE table_name = 'probe' ORDER BY ordinal_position")"
log "записано: $FIXTURES_DIR/$f.txt"
