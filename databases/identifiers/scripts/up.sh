#!/usr/bin/env bash
# up.sh — поднять PG18 + Mongo + Scylla и дождаться готовности.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
docker compose -f compose/compose.yml up -d
echo "[up] ждём PostgreSQL..." >&2
until docker compose -f compose/compose.yml exec -T pg pg_isready -U ids >/dev/null 2>&1; do sleep 1; done
echo "[up] ждём Scylla (CQL)..." >&2
until docker compose -f compose/compose.yml exec -T scylla cqlsh -e 'select now() from system.local' >/dev/null 2>&1; do sleep 2; done
echo "[up] ждём MySQL..." >&2
until docker compose -f compose/compose.yml exec -T mysql mysqladmin ping -uroot -pids --silent >/dev/null 2>&1; do sleep 2; done
echo "[up] готово." >&2
