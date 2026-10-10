#!/usr/bin/env bash
#
# 04-compat.sh <движок> — один и тот же список конструкций, выполненный
# штатным драйвером (pgx для PG-wire, go-sql-driver/mysql для MySQL-wire).
# Эталоны: pg для crdb и yb, mysql для tidb и ob.
#
#   bash scripts/04-compat.sh yb   → fixtures/04-compat-yb.txt

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

e="${1:-}"; [ -n "$e" ] || fail "укажите движок: $ENGINES"
require_running "$e"
f="04-compat-$e"
fixture_header "$f" "$e"
capture "$f" "совместимость" "$(bench_cmd compat -engine "$e")"
log "записано: $FIXTURES_DIR/$f.txt"
