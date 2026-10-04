#!/usr/bin/env bash
# Прогоняет pgbench в трёх режимах synchronous_commit, печатает tps+latency.
set -euo pipefail
PSQL(){ docker compose exec -T postgres psql -U postgres -d waldemo -qtAc "$1"; }
PGB(){ docker compose exec -T postgres pgbench -U postgres -d waldemo "$@"; }

# однократная инициализация масштаба (~15 МБ, scale 100)
PGB -i -s 100 >/dev/null 2>&1 || true

for mode in on local off; do
  PSQL "ALTER SYSTEM SET synchronous_commit = '$mode';" >/dev/null
  PSQL "SELECT pg_reload_conf();" >/dev/null
  echo "=== synchronous_commit = $mode ==="
  # 4 клиента, 20 секунд, отчёт по латентности
  # pgbench 18: релевантные строки отчёта — tps, latency average, number of transactions,
  # без сводных перцентилей (p95/p99 в стандартном отчёте не печатается).
  PGB -c 4 -j 2 -T 20 -r --progress=10 2>&1 | grep -E "^(tps|latency average|number of transactions|initial connection)"
done
PSQL "ALTER SYSTEM SET synchronous_commit = 'on';" >/dev/null; PSQL "SELECT pg_reload_conf();" >/dev/null
