#!/usr/bin/env bash
# Раздел zero-downtime: триггер двойной записи и UPDATE.
#
# Частая форма триггера — «заполнить пустую колонку из заполненной». Для INSERT
# её хватает (это проверяет trigger-timing-demo.sh), для UPDATE — нет: обе
# колонки уже заполнены, и правило ничего не делает. Контрпример:
#   строка 100/100; старая версия приложения пишет amount_cents = 200;
#   total_cents остаётся 100 — новая версия читает устаревшее значение.
#
# Скрипт прогоняет ОДНИ И ТЕ ЖЕ обновления на двух функциях:
#   наивная      — только IS NULL (как в первой редакции стенда);
#   рекомендуемая — sql/sync-trigger.sql: по тому, какую колонку изменил запрос.
# Наивная обязана показать расхождение (иначе контрпример ничего не проверяет),
# рекомендуемая — синхронизировать обе стороны и отвергнуть конфликт.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

require_pg

fresh() {   # свежая таблица с total_cents и одной строкой 100/100
    reset_schema
    psql_c -q -c "ALTER TABLE orders ADD COLUMN total_cents integer;
                  ALTER TABLE orders ALTER COLUMN amount_cents DROP NOT NULL;" >/dev/null
}
row() { psql_q -c "SELECT amount_cents||'/'||total_cents FROM orders WHERE customer='u'"; }

naive_trigger() {
    psql_c -q -v ON_ERROR_STOP=1 -c "
CREATE OR REPLACE FUNCTION sync_amount_total() RETURNS trigger AS \$fn\$
BEGIN
  IF NEW.total_cents IS NULL THEN NEW.total_cents := NEW.amount_cents; END IF;
  RETURN NEW;
END; \$fn\$ LANGUAGE plpgsql;
CREATE TRIGGER trg_sync BEFORE INSERT OR UPDATE ON orders
  FOR EACH ROW EXECUTE FUNCTION sync_amount_total();" >/dev/null
}
good_trigger() {
    docker exec -i "$CONTAINER" psql -U zdt -d zdt -q -v ON_ERROR_STOP=1 \
        < "$ZDT_ROOT/sql/sync-trigger.sql" >/dev/null
}

FAIL=0
check() {   # check <что> <получено> <ожидалось>
    if [ "$2" = "$3" ]; then echo "  ok    $1: $2"
    else echo "  FAIL  $1: $2 (ожидалось $3)"; FAIL=1; fi
}

echo "=== наивная функция (только IS NULL) ==="
fresh; naive_trigger
psql_c -q -c "INSERT INTO orders (customer, amount_cents) VALUES ('u', 100)" >/dev/null
check "INSERT старым кодом"           "$(row)" "100/100"
psql_c -q -c "UPDATE orders SET amount_cents = 200 WHERE customer='u'" >/dev/null
NAIVE="$(row)"
echo "  UPDATE старым кодом, amount_cents = 200: $NAIVE"
check "наивная функция показывает расхождение" "$NAIVE" "200/100"

echo
echo "=== рекомендуемая функция (sql/sync-trigger.sql) ==="
fresh; good_trigger
psql_c -q -c "INSERT INTO orders (customer, amount_cents) VALUES ('u', 100)" >/dev/null
check "INSERT старым кодом"                       "$(row)" "100/100"
psql_c -q -c "UPDATE orders SET amount_cents = 200 WHERE customer='u'" >/dev/null
check "UPDATE старым кодом, amount_cents = 200"   "$(row)" "200/200"
psql_c -q -c "UPDATE orders SET total_cents = 300 WHERE customer='u'" >/dev/null
check "UPDATE новым кодом, total_cents = 300"     "$(row)" "300/300"
psql_c -q -c "UPDATE orders SET amount_cents = 400, total_cents = 400 WHERE customer='u'" >/dev/null
check "UPDATE обеих колонок одним значением"      "$(row)" "400/400"
if psql_c -q -c "UPDATE orders SET amount_cents = 500, total_cents = 600 WHERE customer='u'" >/dev/null 2>&1; then
    check "UPDATE обеих колонок разными значениями" "принят" "отвергнут"
else
    check "UPDATE обеих колонок разными значениями" "отвергнут" "отвергнут"
fi
check "строка после отвергнутого UPDATE"          "$(row)" "400/400"
psql_c -q -c "INSERT INTO orders (customer, total_cents) VALUES ('v', 700)" >/dev/null
check "INSERT новым кодом (только total_cents)" \
    "$(psql_q -c "SELECT amount_cents||'/'||total_cents FROM orders WHERE customer='v'")" "700/700"
psql_c -q -c "INSERT INTO orders (customer, amount_cents, total_cents) VALUES ('w', 800, 800)" >/dev/null
check "INSERT обеих колонок одним значением" \
    "$(psql_q -c "SELECT amount_cents||'/'||total_cents FROM orders WHERE customer='w'")" "800/800"
if psql_c -q -c "INSERT INTO orders (customer, amount_cents, total_cents) VALUES ('conflict', 100, 200)" >/dev/null 2>&1; then
    check "INSERT обеих колонок разными значениями" "принят" "отвергнут"
else
    check "INSERT обеих колонок разными значениями" "отвергнут" "отвергнут"
fi
check "строк 'conflict' после отвергнутого INSERT" \
    "$(psql_q -c "SELECT count(*) FROM orders WHERE customer='conflict'")" "0"

echo
if [ "$FAIL" -ne 0 ]; then echo "ОШИБКА: проверки не прошли." >&2; exit 1; fi
echo "OK: наивная функция теряет UPDATE старого кода (${NAIVE}), рекомендуемая синхронизирует"
echo "обе стороны и отвергает расходящиеся значения обеих колонок — при UPDATE и при INSERT."
