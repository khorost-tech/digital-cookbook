#!/usr/bin/env bash
# Раздел zero-downtime, уровень A. ПАДАЮЩИЙ ВАРИАНТ: наивное переименование
# колонки под нагрузкой роняет работающий код.
#
# Загрузчик идёт по «старой» схеме (amount_cents). В середине окна выполняется
# ALTER TABLE ... RENAME COLUMN amount_cents TO total_cents — мгновенная операция
# на уровне каталога, но с этого момента все запросы старого кода к amount_cents
# падают «column does not exist». Считаем ошибки за окно.
#
# Тезис: zero-downtime ломает не сам факт деплоя, а несовместимое изменение
# СХЕМЫ под работающими экземплярами старого кода.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

require_pg
echo "=== reset схемы к исходной (amount_cents) ==="
reset_schema

BIN="$(mktemp)"
OUT="$(mktemp)"
trap 'rm -f "$BIN" "$OUT"' EXIT
build_loadgen "$BIN"

echo "=== загрузчик по старой схеме (amount_cents), 6с; RENAME на 3-й секунде ==="
"$BIN" -dsn="$DSN" -duration=6s -rps=200 -schema=old > "$OUT" 2>&1 &
LPID=$!

sleep 3
echo "--- ALTER TABLE orders RENAME COLUMN amount_cents TO total_cents (под нагрузкой) ---"
psql_ddl "ALTER TABLE orders RENAME COLUMN amount_cents TO total_cents"

wait "$LPID"

echo
cat "$OUT"
ERRS="$(parse_errors "$OUT")"
DONE="$(parse_done "$OUT")"

echo
echo "ИТОГ: успешно=${DONE}, ошибок=${ERRS} — переименование под нагрузкой уронило"
echo "часть запросов старого кода (обращение к пропавшей amount_cents)."
echo
echo "ПАДАЮЩИЙ ВАРИАНТ (этот): наивный RENAME. Ожидаем ошибок > 0. Рабочий путь —"
echo "expand-contract-demo.sh: то же переименование под той же нагрузкой без ошибок."

# Самопроверка: если ошибок нет, демо не показало проблему.
if [ "${ERRS:-0}" -eq 0 ]; then
    echo "ОШИБКА: ожидались ошибки под нагрузкой, получено 0 — демонстрация не воспроизвела проблему." >&2
    exit 1
fi
echo "OK: наивное переименование под нагрузкой дало ${ERRS} ошибок."
