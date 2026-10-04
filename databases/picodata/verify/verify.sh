#!/usr/bin/env bash
# verify.sh — самопроверка стенда. Падает при любом расхождении, а не печатает
# предупреждение: проверка, которая не умеет ронять прогон, ничего не проверяет.
#
# Сверяются четыре вещи:
#   1) размер категории tools совпадает с эталоном стенда performance/inmemory
#      (33 276) — иначе датасет другой, и сравнивать нечего;
#   2) топ категории от плагина совпадает построчно с топом из источника истины
#      при нескольких значениях limit;
#   3) счёт строк, которые пришлось бы забрать наивным путём, против того, что
#      реально отдаёт плагин;
#   4) команды управления транзакциями в PG-протоколе — заглушки: ROLLBACK
#      отчитывается успешно и ничего не откатывает.
set -euo pipefail

CATEGORY="${CATEGORY:-tools}"
EXPECTED_TOOLS=33276
ORIGIN_DSN="${ORIGIN_DSN:?нужен ORIGIN_DSN}"
PICO_HTTP="${PICO_HTTP:-http://127.0.0.1:8081}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

PY="$(command -v python3 || command -v python || true)"
[ -n "$PY" ] || { echo "!!! нужен python для разбора ответа плагина" >&2; exit 1; }

# psql запускается в контейнере источника: локальный клиент не требуется.
psql_origin() {
    MSYS_NO_PATHCONV=1 docker exec -i picodata-origin \
        psql "postgres://picodata:picodata@127.0.0.1:5432/catalog" "$@"
}

# Тот же самый psql — но к Picodata. В этом и смысл PG-протокола.
psql_pico() {
    MSYS_NO_PATHCONV=1 docker exec -i picodata-origin psql "$PICO_DSN" "$@"
}

echo "=== 1. размер категории ==="
tools_count="$(psql_origin -tAc \
    "SELECT count(*) FROM products WHERE attrs->>'category' = '${CATEGORY}'")"
tools_count="$(echo "$tools_count" | tr -d '[:space:]')"
if [ "$CATEGORY" = "tools" ] && [ "$tools_count" != "$EXPECTED_TOOLS" ]; then
    echo "!!! категория tools = $tools_count, ожидалось $EXPECTED_TOOLS — другой датасет" >&2
    exit 1
fi
echo "  ${CATEGORY}: $tools_count товаров"

echo "=== 2. топ плагина против источника истины ==="
for lim in 5 10 25; do
    origin_top="$(psql_origin -tA -F'|' \
        -v "category=${CATEGORY}" -v "lim=${lim}" -f /dev/stdin \
        < "$SCRIPT_DIR/compare.sql" | tr -d '\r')"

    # Ответ плагина разбирается python-ом, а не jq: jq есть не в каждом
    # окружении, python нужен репозиторию и без этого стенда.
    plugin_top="$(curl -sf --max-time 60 \
        "${PICO_HTTP}/top_products?category=${CATEGORY}&limit=${lim}" \
        | "$PY" "$SCRIPT_DIR/topline.py" | tr -d '\r')"

    if [ "$origin_top" != "$plugin_top" ]; then
        echo "!!! топ-${lim} расходится" >&2
        echo "--- источник истины ---" >&2; echo "$origin_top" >&2
        echo "--- плагин ---"          >&2; echo "$plugin_top" >&2
        exit 1
    fi
    n="$(echo "$plugin_top" | grep -c '|')"
    if [ "$n" -ne "$lim" ]; then
        echo "!!! плагин вернул $n строк вместо $lim" >&2
        exit 1
    fi
    echo "  limit=${lim}: совпало построчно ($n строк)"
done

echo "=== 3. сколько строк уезжает наружу ==="
echo "  наивный путь (вся категория клиенту): ${tools_count}"
echo "  плагин при limit=5:                   5"
echo "  отношение:                            $((tools_count / 5))"

echo "=== 4. транзакции в PG-протоколе — заглушки ==="
# Статья утверждает, что BEGIN/COMMIT/ROLLBACK приняты как заглушки и работа
# идёт в autocommit. Утверждение о поведении обязано проверяться, а не жить
# только в тексте, поэтому оно здесь.
#
# ВНИМАНИЕ к трактовке провала: если однажды ROLLBACK начнёт откатывать
# по-настоящему, этот блок упадёт — и это НЕ поломка стенда, а сигнал, что
# поведение Picodata изменилось и текст статьи требует пересмотра.
psql_pico -q -c "DROP TABLE tx_probe" >/dev/null 2>&1 || true
psql_pico -q -c "CREATE TABLE tx_probe (id UNSIGNED NOT NULL, PRIMARY KEY (id)) DISTRIBUTED BY (id)" >/dev/null

tx_left="$(psql_pico -tA <<'SQL' 2>/dev/null | tail -1
BEGIN;
INSERT INTO tx_probe VALUES (1);
ROLLBACK;
SELECT count(*) FROM tx_probe;
SQL
)"
tx_left="$(echo "$tx_left" | tr -d '[:space:]')"
psql_pico -q -c "DROP TABLE tx_probe" >/dev/null

case "$tx_left" in
    1) echo "  ok: после ROLLBACK строка на месте (count=1) — откат не произошёл" ;;
    0) echo "!!! ROLLBACK ОТКАТИЛ вставку (count=0): поведение изменилось," >&2
       echo "    утверждение статьи о заглушках больше не верно" >&2
       exit 1 ;;
    *) echo "!!! неожиданный результат проверки транзакций: '${tx_left}'" >&2
       exit 1 ;;
esac

echo
echo "ok: топ плагина совпадает с источником истины на всех проверенных limit,"
echo "    транзакции ведут себя как задокументировано"
