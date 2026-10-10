#!/usr/bin/env bash
#
# probe.sh — версии всего, что участвует в замерах.
#   bash scripts/probe.sh   → fixtures/00-probe.txt
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

f=00-probe
fixture_header "$f"
$COMPOSE up -d --wait ts prom vm >/dev/null 2>&1 || fail "сервисы не поднялись"
capture "$f" "PostgreSQL и TimescaleDB" "docker exec tsdb-ts psql -U postgres -d ts -c \"SELECT version()\" -c \"SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'\""
capture "$f" "Prometheus" "docker exec tsdb-prom prometheus --version"
capture "$f" "VictoriaMetrics" "docker exec tsdb-vm /victoria-metrics-prod -version"
capture "$f" "генератор: Go и зависимости" "grep -E '^go |pgx|snappy|protobuf' gen/go.mod"
log "записано: $FIXTURES_DIR/$f.txt"
