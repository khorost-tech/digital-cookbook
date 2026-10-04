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
#      выполняется вовсе — это практическая цена неверного ключа шардирования;
#   5) поднятый для одного запроса лимит переноса снимает именно это
#      ограничение, но работающий запрос не гарантирует: на этом стенде тот же
#      JOIN упирается в следующий предел — sql_vdbe_opcode_max (наблюдалось
#      вплоть до 2 000 000 000 опкодов при a.id < 10: соединение без коллокации
#      перебирает пары строк). Если когда-нибудь запрос пройдёт, результат
#      сверяется с PostgreSQL.
#
#   6) шаг 8 разводит три фактора на одинаковом фильтре a.id < 10: соединение
#      по id (коллокация, 1 пара на строку), по уникальному sku (без коллокации,
#      тоже 1 пара) и по category (без коллокации, ~33 тыс. пар на строку).
#      Отказ по переносу — цена отсутствия коллокации, и фильтр слева его не
#      уменьшает: переносится вся правая сторона. Упор в опкоды — свойство
#      соединения по category, а не любого соединения без коллокации.
#
# Шаги 6–8 — проверки, а не только печать: каждый исход обязан быть именно
# тем, что описано выше (по тексту ошибки или сверке с PostgreSQL), иначе
# скрипт завершается с ошибкой.
# Планы печатаются как есть: их формат — часть ответа, а не оформление.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
ORIGIN_DSN="${ORIGIN_DSN:-postgres://picodata:picodata@127.0.0.1:5432/catalog}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"

# SQL подаётся на stdin, а не аргументом: через аргумент он прошёл бы лишний
# слой shell-квотинга и кавычки внутри запроса сменили бы смысл.
run() {
    printf '%s\n' "$2" | MSYS_NO_PATHCONV=1 docker exec -i picodata-origin \
        psql "$PICO_DSN" -tA -v ON_ERROR_STOP=1 -f - 2>&1
}
run_origin() {
    printf '%s\n' "$1" | MSYS_NO_PATHCONV=1 docker exec -i picodata-origin \
        psql "$ORIGIN_DSN" -tA -v ON_ERROR_STOP=1 -f - 2>&1
}
fail=0
# Вывод — в отслеживаемый plans.txt рядом со скриптом (out/ в .gitignore).
exec > >(tee "$DIR/plans.txt") 2>&1

JOIN_WIDE="SELECT count(*) FROM products a JOIN products b ON a.category = b.category WHERE a.id < 2000"

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
out6="$(run p6 "$JOIN_WIDE")"; rc6=$?
echo "$out6"
if [ "$rc6" -ne 0 ] && printf '%s' "$out6" | grep -q 'Exceeded maximum number of rows'; then
    echo "ok: отказ, и причина — лимит переноса sql_motion_row_max (код $rc6)"
else
    echo "!!! ожидался отказ по sql_motion_row_max, получено: код $rc6" >&2
    fail=1
fi

echo
echo "=== 7. тот же запрос с поднятым для него лимитом переноса ==="
# OPTION действует только на этот запрос и не меняет настройку кластера.
out7="$(run p7 "$JOIN_WIDE OPTION (SQL_MOTION_ROW_MAX = 200000)")"; rc7=$?
echo "$out7"
# Эталон — тот же запрос в источнике истины (там category лежит в attrs).
want="$(run_origin "SELECT count(*) FROM products a JOIN products b ON a.attrs->>'category' = b.attrs->>'category' WHERE a.id < 2000")"
if [ "$rc7" -ne 0 ] && printf '%s' "$out7" | grep -q 'max executed vdbe opcodes'; then
    echo "ok: лимит переноса снят, но запрос упёрся в следующий предел — sql_vdbe_opcode_max (код $rc7)"
    echo "    (эталон PostgreSQL для этого запроса: ${want} строк в соединении — count(*) по ним)"
elif [ "$rc7" -ne 0 ]; then
    echo "!!! с поднятым лимитом запрос не выполнился по другой причине (код $rc7)" >&2
    fail=1
elif [ "$out7" = "$want" ]; then
    echo "ok: выполнился, count(*) совпадает с PostgreSQL (${want})"
else
    echo "!!! выполнился, но результат ${out7} не совпадает с PostgreSQL (${want})" >&2
    fail=1
fi

echo
echo "=== 8. три соединения на одном фильтре a.id < 10: что именно стоит дорого ==="
# expect: ok — выполнился и совпал с PostgreSQL; motion / vdbe — отказ именно
# по этому лимиту. Эталон PostgreSQL — тот же запрос в источнике истины.
check_join() { # <подпись> <ON для Picodata> <ON для PostgreSQL> <OPTION или ""> <expect>
    local q="SELECT count(*) FROM products a JOIN products b ON $2 WHERE a.id < 10"
    local o out rc want
    [ -n "$4" ] && o=" OPTION ($4)" || o=""
    out="$(run p8 "$q$o")"; rc=$?
    want="$(run_origin "SELECT count(*) FROM products a JOIN products b ON $3 WHERE a.id < 10")"
    local got
    if [ "$rc" -eq 0 ]; then got="ok:$out"
    elif printf '%s' "$out" | grep -q 'Exceeded maximum number of rows'; then got="motion"
    elif printf '%s' "$out" | grep -q 'max executed vdbe opcodes'; then got="vdbe"
    else got="другая ошибка"; fi
    printf "  %-40s %-30s -> %-12s (PostgreSQL: %s)\n" "$1" "${4:-лимиты по умолчанию}" "$got" "$want"
    case "$5" in
        ok)  [ "$got" = "ok:$want" ] || { echo "  !!! ожидался успех с результатом $want" >&2; fail=1; } ;;
        *)   [ "$got" = "$5" ]       || { echo "  !!! ожидался отказ по $5" >&2; fail=1; } ;;
    esac
}
RAISE="SQL_MOTION_ROW_MAX = 200000"
check_join "по id (коллокация)"            "a.id = b.id"             "a.id = b.id" ""       ok
check_join "по sku (без коллокации, 1:1)"  "a.sku = b.sku"           "a.sku = b.sku" ""     motion
check_join "по sku (без коллокации, 1:1)"  "a.sku = b.sku"           "a.sku = b.sku" "$RAISE" ok
check_join "по category (без коллокации)"  "a.category = b.category" "a.attrs->>'category' = b.attrs->>'category'" ""       motion
check_join "по category (без коллокации)"  "a.category = b.category" "a.attrs->>'category' = b.attrs->>'category'" "$RAISE" vdbe

exit "$fail"
