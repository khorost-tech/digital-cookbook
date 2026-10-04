#!/usr/bin/env bash
# plans.sh — снимает планы распределённых запросов Picodata и сохраняет их
# дословно в out/plans.txt.
#
# Что показывают планы:
#   1) запрос по ключу шардирования адресуется в ОДИН бакет, без ключа — во все;
#   2) агрегация разбивается на две фазы: локальную на хранилищах и финальную
#      на роутере, между ними motion переносит промежуточный результат;
#   3) соединение по ключу шардирования обходится БЕЗ motion (коллокация),
#      а по обычной колонке требует переноса всей правой стороны;
#   4) перенос ограничен sql_motion_row_max, и на объёме такой запрос не
#      выполняется вовсе — это практическая цена неверного ключа шардирования.
#
# Планы печатаются как есть: их формат — часть ответа, а не оформление.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$DIR/out"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"

# SQL подаётся на stdin, а не аргументом: через аргумент он прошёл бы лишний
# слой shell-квотинга и кавычки внутри запроса сменили бы смысл.
run() {
    printf '%s\n' "$2" | MSYS_NO_PATHCONV=1 docker exec -i picodata-origin \
        psql "$PICO_DSN" -tA -v ON_ERROR_STOP=1 -f - 2>&1
}

{
    echo "=== 1. точечный запрос по ключу шардирования ==="
    run p1 "EXPLAIN SELECT id, title FROM products WHERE id = 42"

    echo
    echo "=== 2. тот же запрос по обычной колонке ==="
    run p2 "EXPLAIN SELECT id FROM products WHERE category = 'tools'"

    echo
    echo "=== 3. агрегация: две фазы и motion между ними ==="
    run p3 "EXPLAIN SELECT category, count(*) FROM products GROUP BY category"

    echo
    echo "=== 4. соединение ПО ключу шардирования — коллокация, motion нет ==="
    run p4 "EXPLAIN SELECT a.id FROM products a JOIN products b ON a.id = b.id WHERE a.id < 10"

    echo
    echo "=== 5. соединение по обычной колонке — motion full ==="
    run p5 "EXPLAIN SELECT a.id FROM products a JOIN products b ON a.category = b.category WHERE a.id < 10"

    echo
    echo "=== 6. цена переноса: тот же запрос на объёме ==="
    run p6 "SELECT count(*) FROM products a JOIN products b ON a.category = b.category WHERE a.id < 2000"
    echo "(ожидается отказ: перенос не помещается в sql_motion_row_max)"
} | tee "$DIR/out/plans.txt"
