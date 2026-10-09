#!/usr/bin/env bash
#
# 03-isolation.sh <движок> — write skew («дежурные врачи») в трёх вариантах:
#
#   default       уровень изоляции движка по умолчанию, обычный SELECT
#   serializable  явно запрошенный SERIALIZABLE
#   for-update    уровень по умолчанию, но читаемые строки блокируются
#
# Итог каждого прогона — сколько врачей осталось на дежурстве. Ноль значит,
# что обе транзакции закоммитились и нарушили инвариант.
#
#   bash scripts/03-isolation.sh tidb   → fixtures/03-isolation-tidb.txt

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: $ENGINES"
require_running "$e"
f="03-isolation-$e"
fixture_header "$f" "$e"

isos="default serializable for-update"
# TiDB отклоняет SERIALIZABLE и в тексте ошибки предлагает флаг
# tidb_skip_isolation_level_check. Проверяем, что он делает на самом деле.
[ "$e" = tidb ] && isos="$isos skipcheck"
for iso in $isos; do
    capture "$f" "write skew: $iso" "$(bench_cmd skew -engine "$e" -iso "$iso")"
done
log "записано: $FIXTURES_DIR/$f.txt"
