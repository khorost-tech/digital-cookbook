#!/usr/bin/env bash
# results.sh — сравнивает РЕЗУЛЬТАТЫ запросов Picodata с PostgreSQL и проверяет
# постусловия DML. Падает при первом же расхождении.
#
# Зачем отдельно от probes.tsv: та матрица показывает, принят ли синтаксис.
# Принят — не значит «даёт тот же ответ». Без этой проверки фраза «конструкция
# работает» опиралась бы только на отсутствие ошибки, а это слабее, чем звучит.
#
# Пары запросов различаются текстом, потому что различается схема источника и
# кластера; сравнивается построчный вывод.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$DIR/out"
export MSYS_NO_PATHCONV=1
exec > >(tee "$DIR/out/results.txt") 2>&1

ORIGIN_DSN="${ORIGIN_DSN:-postgres://picodata:picodata@127.0.0.1:5432/catalog}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"
PAIRS="${1:-$DIR/results.tsv}"
fail=0

# SQL подаётся на stdin: через аргумент он прошёл бы лишний слой квотинга.
run_pg()   { printf '%s\n' "$1" | docker exec -i picodata-origin psql "$ORIGIN_DSN" -tA -F'|' -v ON_ERROR_STOP=1 -f - 2>&1 | tr -d '\r'; }
run_pico() { printf '%s\n' "$1" | docker exec -i picodata-origin psql "$PICO_DSN"   -tA -F'|' -v ON_ERROR_STOP=1 -f - 2>&1 | tr -d '\r'; }

echo "=== 1. совпадение результатов с PostgreSQL ==="
while IFS=$'\t' read -r id sql_pg sql_pico; do
    case "$id" in ''|\#*) continue ;; esac
    [ -n "${sql_pico:-}" ] || continue

    out_pg="$(run_pg "$sql_pg")"
    out_pico="$(run_pico "$sql_pico")"

    if [ "$out_pg" = "$out_pico" ]; then
        rows="$(printf '%s' "$out_pg" | grep -c '' )"
        printf "  %-22s совпало (%s строк)\n" "$id" "$rows"
    else
        printf "  %-22s РАСХОЖДЕНИЕ\n" "$id"
        echo "    PostgreSQL: $(printf '%s' "$out_pg"   | head -3 | tr '\n' ' ')"
        echo "    Picodata:   $(printf '%s' "$out_pico" | head -3 | tr '\n' ' ')"
        fail=1
    fi
done < "$PAIRS"

echo
echo "=== 2. постусловия DML ==="
# Проверяется не «команда принята», а то, что она изменила данные ожидаемым
# образом: после INSERT строка читается, после UPDATE значение новое, после
# DELETE строки нет.
run_pico "DROP TABLE dml_probe" >/dev/null 2>&1
run_pico "CREATE TABLE dml_probe (id UNSIGNED NOT NULL, title TEXT NOT NULL, PRIMARY KEY (id)) USING memtx DISTRIBUTED BY (id)" >/dev/null

check() { # <шаг> <ожидаемое> <sql>
    got="$(run_pico "$3" | tr -d '[:space:]')"
    if [ "$got" = "$2" ]; then
        printf "  %-28s ok (%s)\n" "$1" "$got"
    else
        printf "  %-28s ОЖИДАЛОСЬ %s, ПОЛУЧЕНО '%s'\n" "$1" "$2" "$got"
        fail=1
    fi
}

run_pico "INSERT INTO dml_probe (id, title) VALUES (1, 'a'), (2, 'b')" >/dev/null
check "после INSERT: строк"        "2" "SELECT count(*) FROM dml_probe"
check "после INSERT: значение"     "a" "SELECT title FROM dml_probe WHERE id = 1"

run_pico "UPDATE dml_probe SET title = 'z' WHERE id = 1" >/dev/null
check "после UPDATE: новое значение" "z" "SELECT title FROM dml_probe WHERE id = 1"
check "после UPDATE: соседняя строка" "b" "SELECT title FROM dml_probe WHERE id = 2"

run_pico "DELETE FROM dml_probe WHERE id = 1" >/dev/null
check "после DELETE: строк"        "1" "SELECT count(*) FROM dml_probe"
check "после DELETE: строки нет"   "0" "SELECT count(*) FROM dml_probe WHERE id = 1"

run_pico "TRUNCATE TABLE dml_probe" >/dev/null
check "после TRUNCATE: строк"      "0" "SELECT count(*) FROM dml_probe"

run_pico "DROP TABLE dml_probe" >/dev/null

echo
if [ "$fail" -ne 0 ]; then
    echo "ПРОВАЛ: результаты расходятся с PostgreSQL или постусловия не выполнены" >&2
    exit 1
fi
echo "ok: результаты совпадают с PostgreSQL, постусловия DML выполняются"
