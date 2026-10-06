#!/usr/bin/env bash
# migrator.sh — goose против Picodata.
#
# Мигратор опирается на две вещи, которых у Picodata нет в привычном виде:
# транзакционный DDL и системные каталоги, по которым он находит собственную
# таблицу версий. Сама миграция намеренно тривиальна — проверяется не она, а
# способность инструмента вести журнал версий.
#
# Скрипт сохраняет фактический вывод, каким бы он ни оказался, и НЕ считает
# провал мигратора ошибкой стенда.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
DIR_DOCKER="$(cd "$(dirname "$0")" && (pwd -W 2>/dev/null || pwd))"
mkdir -p "$DIR/out"
export MSYS_NO_PATHCONV=1

DSN="postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable"
GOOSE_VERSION="${GOOSE_VERSION:-v3.22.1}"

{
    echo "=== goose ${GOOSE_VERSION} up ==="
    docker run --rm --network picodata-net -v "${DIR_DOCKER}/migrations:/mig" \
        golang:1.26.4 sh -c \
        "go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION} >/dev/null 2>&1 && \
         goose -dir /mig postgres '${DSN}' up" 2>&1 | tail -25
    echo "код возврата: ${PIPESTATUS[0]}"

    echo
    echo "=== появилась ли таблица миграции ==="
    # Метакоманда psql \dt здесь не годится: она разворачивается в запрос к
    # pg_catalog (pg_get_userbyid) и падает так же, как pg_dump. Список таблиц
    # берём из системного представления самой Picodata.
    docker exec picodata-origin psql "$DSN" -tAc \
        "SELECT name FROM _pico_table WHERE name IN ('migration_probe', 'goose_db_version')" 2>&1 | tail -5
    echo "(пусто — значит не создалась ни миграция, ни журнал версий goose)"
} | tee "$DIR/out/migrator.txt"
