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
#
# Совпадение засчитывается, только если ОБА запроса выполнились успешно (код
# завершения psql 0) и вернули непустой результат. Сравнивается только stdout —
# данные; stderr в сравнение не попадает. Иначе одинаковая ошибка на обеих
# сторонах (или два пустых ответа) была бы объявлена совпадением.
set -uo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$DIR/out"
export MSYS_NO_PATHCONV=1
# Вывод — в отслеживаемый results.txt рядом со скриптом (не в out/, который в
# .gitignore): на него ссылается статья, и он должен быть виден в репозитории.
exec > >(tee "$DIR/results.txt") 2>&1

ORIGIN_DSN="${ORIGIN_DSN:-postgres://picodata:picodata@127.0.0.1:5432/catalog}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"
PAIRS="${1:-$DIR/results.tsv}"
fail=0

# SQL подаётся на stdin: через аргумент он прошёл бы лишний слой квотинга.
# run_q <dsn> <sql>: данные -> $OUT, ошибки -> $ERR, код завершения psql -> $RC.
# Под pipefail $RC ненулевой, если упал psql (ON_ERROR_STOP=1) или docker exec.
ERRF="$(mktemp)"
trap 'rm -f "$ERRF"' EXIT
run_q() {
    OUT="$(printf '%s\n' "$2" | docker exec -i picodata-origin psql "$1" -tA -F'|' -v ON_ERROR_STOP=1 -f - 2>"$ERRF" | tr -d '\r')"
    RC=$?
    ERR="$(tr -d '\r' < "$ERRF" | head -3 | tr '\n' ' ')"
}
# Для постусловий DML: шаг обязан выполниться, иначе проверка недействительна.
must_pico() {
    run_q "$PICO_DSN" "$1"
    if [ "$RC" -ne 0 ]; then
        printf "  %-28s ШАГ НЕ ВЫПОЛНЕН (код %s): %s\n" "$1" "$RC" "$ERR"
        fail=1
    fi
}

echo "=== 1. совпадение результатов с PostgreSQL ==="
while IFS=$'\t' read -r id sql_pg sql_pico; do
    case "$id" in ''|\#*) continue ;; esac
    [ -n "${sql_pico:-}" ] || continue

    run_q "$ORIGIN_DSN" "$sql_pg";   out_pg="$OUT";   rc_pg=$RC;   err_pg="$ERR"
    run_q "$PICO_DSN"   "$sql_pico"; out_pico="$OUT"; rc_pico=$RC; err_pico="$ERR"

    if [ "$rc_pg" -ne 0 ] || [ "$rc_pico" -ne 0 ]; then
        printf "  %-22s ЗАПРОС НЕ ВЫПОЛНЕН (код PostgreSQL %s, Picodata %s)\n" "$id" "$rc_pg" "$rc_pico"
        [ "$rc_pg" -eq 0 ]   || echo "    PostgreSQL: $err_pg"
        [ "$rc_pico" -eq 0 ] || echo "    Picodata:   $err_pico"
        fail=1
    elif [ -z "$out_pg" ]; then
        printf "  %-22s ПУСТОЙ ОТВЕТ — сравнивать нечего, пара ничего не доказывает\n" "$id"
        fail=1
    elif [ "$out_pg" = "$out_pico" ]; then
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
run_q "$PICO_DSN" "DROP TABLE dml_probe"   # может не существовать — код не проверяем
must_pico "CREATE TABLE dml_probe (id UNSIGNED NOT NULL, title TEXT NOT NULL, PRIMARY KEY (id)) USING memtx DISTRIBUTED BY (id)"

check() { # <шаг> <ожидаемое> <sql>
    run_q "$PICO_DSN" "$3"
    got="$(printf '%s' "$OUT" | tr -d '[:space:]')"
    if [ "$RC" -ne 0 ]; then
        printf "  %-28s ЗАПРОС НЕ ВЫПОЛНЕН (код %s): %s\n" "$1" "$RC" "$ERR"
        fail=1
    elif [ "$got" = "$2" ]; then
        printf "  %-28s ok (%s)\n" "$1" "$got"
    else
        printf "  %-28s ОЖИДАЛОСЬ %s, ПОЛУЧЕНО '%s'\n" "$1" "$2" "$got"
        fail=1
    fi
}

must_pico "INSERT INTO dml_probe (id, title) VALUES (1, 'a'), (2, 'b')"
check "после INSERT: строк"        "2" "SELECT count(*) FROM dml_probe"
check "после INSERT: значение"     "a" "SELECT title FROM dml_probe WHERE id = 1"

must_pico "UPDATE dml_probe SET title = 'z' WHERE id = 1"
check "после UPDATE: новое значение" "z" "SELECT title FROM dml_probe WHERE id = 1"
check "после UPDATE: соседняя строка" "b" "SELECT title FROM dml_probe WHERE id = 2"

must_pico "DELETE FROM dml_probe WHERE id = 1"
check "после DELETE: строк"        "1" "SELECT count(*) FROM dml_probe"
check "после DELETE: строки нет"   "0" "SELECT count(*) FROM dml_probe WHERE id = 1"

must_pico "TRUNCATE TABLE dml_probe"
check "после TRUNCATE: строк"      "0" "SELECT count(*) FROM dml_probe"

must_pico "DROP TABLE dml_probe"

echo
if [ "$fail" -ne 0 ]; then
    echo "ПРОВАЛ: результаты расходятся с PostgreSQL или постусловия не выполнены" >&2
    exit 1
fi
echo "ok: все запросы выполнились, результаты совпадают с PostgreSQL, постусловия DML выполняются"
