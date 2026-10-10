#!/usr/bin/env bash
# pg_dump.sh — самый частый вопрос при переезде: снимется ли дамп штатным
# инструментом PostgreSQL.
#
# Скрипт НЕ утверждает заранее, чем всё кончится, — он сохраняет ровно то, что
# ответил pg_dump, вместе с кодом возврата. Провал инструмента здесь не провал
# стенда: отрицательный результат такой же результат, и именно он попадёт в
# статью, если случится.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$DIR/out"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"

{
    echo "=== версия pg_dump ==="
    MSYS_NO_PATHCONV=1 docker exec picodata-origin pg_dump --version 2>&1

    echo
    echo "=== pg_dump --schema-only ==="
    MSYS_NO_PATHCONV=1 docker exec picodata-origin \
        pg_dump "$PICO_DSN" --schema-only 2>&1 | head -30
    echo "код возврата: ${PIPESTATUS[0]}"

    echo
    echo "=== pg_dump --data-only --table=products ==="
    MSYS_NO_PATHCONV=1 docker exec picodata-origin \
        pg_dump "$PICO_DSN" --data-only --table=products 2>&1 | head -20
    echo "код возврата: ${PIPESTATUS[0]}"
} | tee "$DIR/out/pg_dump.txt"
