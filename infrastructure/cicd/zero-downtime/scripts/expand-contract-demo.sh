#!/usr/bin/env bash
# Раздел zero-downtime, уровень A. РАБОЧИЙ ВАРИАНТ: то же переименование
# amount_cents -> total_cents под той же нагрузкой, но через expand-contract —
# без единой ошибки.
#
# Переход изображён так, как он идёт в реальности: тремя версиями кода, каждая
# выкатывается поверх работающей предыдущей (окна сосуществования):
#   old  — пишет/читает amount_cents;
#   dual — промежуточная выкатка: пишет ОБЕ колонки, читает старую;
#   new  — пишет/читает total_cents.
#
# Шаги схемы:
#   1. EXPAND   — ADD COLUMN total_cents (nullable) и снятие NOT NULL с
#      amount_cents. Оба изменения совместимы со ВСЕМИ версиями кода сразу:
#      старый код продолжает писать amount_cents, новый сможет не писать его.
#   2. выкатка DUAL поверх работающего old (окно old+dual);
#   3. BACKFILL старых строк батчами (total_cents IS NULL), когда old уже ушёл;
#   4. выкатка NEW поверх работающего dual (окно dual+new);
#   5. CONTRACT — когда ни old, ни dual не осталось, удаляем amount_cents;
#   6. восстановление NOT NULL на total_cents — иначе итоговая схема осталась бы
#      слабее той, к которой приводит наивный RENAME, и сравнение способов было
#      бы нечестным. Проверяется автоматически в конце скрипта.
#
# dual-write делает САМ код (как при настоящем деплое промежуточной версии), а
# не триггер БД. Важно: это НЕ потому, что триггер «подхватывается с задержкой» —
# такого эффекта нет, см. trigger-timing-demo.sh, где 8 конкурентных соединений,
# открытых до CREATE TRIGGER, после его коммита не пропустили ни одной вставки.
# Выбор в пользу кода здесь сделан потому, что он показывает реальную
# последовательность деплоев; для разовой миграции триггер бывает практичнее.
#
# Единственная переменная против naive-rename-demo.sh — способ перехода.
# Нагрузка та же.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

require_pg
echo "=== reset схемы к исходной (amount_cents) ==="
reset_schema

BIN="$(mktemp)"
OUT_OLD="$(mktemp)"; OUT_DUAL="$(mktemp)"; OUT_NEW="$(mktemp)"
trap 'rm -f "$BIN" "$OUT_OLD" "$OUT_DUAL" "$OUT_NEW"' EXIT
build_loadgen "$BIN"

echo "=== старый код (amount_cents) под нагрузкой, 4с ==="
"$BIN" -dsn="$DSN" -duration=4s -rps=200 -schema=old > "$OUT_OLD" 2>&1 &
OLD_PID=$!
sleep 1

echo "--- шаг 1: EXPAND — ADD COLUMN total_cents (nullable), снять NOT NULL с amount_cents ---"
psql_ddl "ALTER TABLE orders ADD COLUMN total_cents integer"
psql_ddl "ALTER TABLE orders ALTER COLUMN amount_cents DROP NOT NULL"

echo "--- шаг 2: выкатка DUAL (пишет обе колонки) поверх работающего old, 4с ---"
"$BIN" -dsn="$DSN" -duration=4s -rps=200 -schema=dual > "$OUT_DUAL" 2>&1 &
DUAL_PID=$!

echo "--- ждём завершения old (окно old+dual закрывается) ---"
wait "$OLD_PID"

echo "--- шаг 3: BACKFILL старых строк батчами (total_cents IS NULL) ---"
BATCHES=0
while :; do
    n="$(psql_q -c "WITH b AS (SELECT id FROM orders WHERE total_cents IS NULL LIMIT 1000 FOR UPDATE SKIP LOCKED) UPDATE orders o SET total_cents = o.amount_cents FROM b WHERE o.id = b.id RETURNING o.id" | grep -c '^[0-9]' || true)"
    [ "$n" -eq 0 ] && break
    BATCHES=$((BATCHES + 1))
done
echo "backfill завершён за ${BATCHES} батчей"

echo "--- шаг 4: выкатка NEW (total_cents) поверх работающего dual, 3с ---"
"$BIN" -dsn="$DSN" -duration=3s -rps=200 -schema=new > "$OUT_NEW" 2>&1 &
NEW_PID=$!

echo "--- ждём завершения dual (окно dual+new закрывается) ---"
wait "$DUAL_PID"

echo "--- шаг 5: CONTRACT — удаляем amount_cents (ни old, ни dual уже нет) ---"
psql_ddl "ALTER TABLE orders DROP COLUMN amount_cents"

echo "--- шаг 6: восстановить NOT NULL на total_cents (иначе схема НЕ эквивалентна RENAME) ---"
# Наивный RENAME сохраняет исходный NOT NULL, а expand начинался с nullable-колонки.
# Без этого шага expand-contract заканчивался бы ОСЛАБЛЕННОЙ схемой, и сравнение
# двух способов было бы нечестным: они дали бы разный результат.
#
# Прямой SET NOT NULL на большой таблице сканирует её целиком под ACCESS EXCLUSIVE
# — то есть ровно тот простой, которого мы избегаем. Последовательность ниже
# убирает СКАН из-под тяжёлой блокировки:
#   1. CHECK ... NOT VALID  — существующие строки не проверяются;
#   2. VALIDATE CONSTRAINT  — сканирует под SHARE UPDATE EXCLUSIVE, не блокируя
#      запись и чтение;
#   3. SET NOT NULL         — PostgreSQL 12+ видит валидный CHECK и таблицу
#      заново НЕ сканирует;
#   4. CHECK больше не нужен — снимаем.
#
# ⚠️ Отсутствие скана НЕ равно отсутствию простоя. Шаг 3 всё равно берёт
# ACCESS EXCLUSIVE, а значит: ждёт завершения всех текущих транзакций по таблице
# (включая долгие SELECT и забытые «idle in transaction») и, пока ждёт, копит за
# собой очередь новых запросов. Поэтому здесь стоит lock_timeout и повтор: лучше
# отступить и попробовать позже, чем встать в очередь на неопределённое время.
# Стенд однопользовательский, так что на нём это ветка страховки, а не замер.
psql_ddl "ALTER TABLE orders ADD CONSTRAINT total_cents_not_null CHECK (total_cents IS NOT NULL) NOT VALID"

# VALIDATE берёт SHARE UPDATE EXCLUSIVE — чтение и запись не блокирует,
# поэтому lock_timeout ему не нужен; дорогой скан таблицы идёт именно здесь.
psql_c -c "ALTER TABLE orders VALIDATE CONSTRAINT total_cents_not_null" >/dev/null

psql_ddl "ALTER TABLE orders ALTER COLUMN total_cents SET NOT NULL"

psql_ddl "ALTER TABLE orders DROP CONSTRAINT total_cents_not_null"

wait "$NEW_PID"

echo
echo "=== результаты всех трёх версий кода ==="
echo "old:  $(head -1 "$OUT_OLD")"
echo "dual: $(head -1 "$OUT_DUAL")"
echo "new:  $(head -1 "$OUT_NEW")"

ERRS_OLD="$(parse_errors "$OUT_OLD")"
ERRS_DUAL="$(parse_errors "$OUT_DUAL")"
ERRS_NEW="$(parse_errors "$OUT_NEW")"
TOTAL_ERRS=$((ERRS_OLD + ERRS_DUAL + ERRS_NEW))

echo
echo "ИТОГ: ошибок old=${ERRS_OLD}, dual=${ERRS_DUAL}, new=${ERRS_NEW}, всего=${TOTAL_ERRS}"
echo "backfill: ${BATCHES} батчей."
echo
echo "Сравнение с naive-rename-demo.sh: та же нагрузка, то же переименование,"
echo "но expand-contract прошёл без ошибок, а наивный RENAME уронил сотни запросов."

if [ "$TOTAL_ERRS" -ne 0 ]; then
    echo "ОШИБКА: expand-contract дал ${TOTAL_ERRS} ошибок — переход НЕ был бесшовным." >&2
    exit 1
fi

# Проверка ЭКВИВАЛЕНТНОСТИ СХЕМЫ. Без неё демо доказывало бы лишь «перешли без
# ошибок», умалчивая, что пришли к другой схеме: наивный RENAME сохраняет
# NOT NULL, а expand начинается с nullable-колонки. Сравнение способов честно
# только при одинаковом результате.
SCHEMA="$(psql_q -c "SELECT column_name||':'||is_nullable FROM information_schema.columns
                     WHERE table_name='orders' AND column_name IN ('amount_cents','total_cents')
                     ORDER BY column_name")"
echo
echo "итоговая схема: ${SCHEMA//$'\n'/, }"
if [ "$SCHEMA" != "total_cents:NO" ]; then
    echo "ОШИБКА: итоговая схема не совпадает с результатом наивного RENAME." >&2
    echo "Ожидалось ровно 'total_cents:NO' (колонка есть и она NOT NULL), получено: ${SCHEMA}" >&2
    exit 1
fi
echo "OK: переименование под нагрузкой прошло без ошибок, итоговая схема эквивалентна RENAME."
