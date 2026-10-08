#!/usr/bin/env bash
#
# up.sh — собрать генератор и поднять сервисы стенда.
#   bash scripts/up.sh            → ts, prom, vm
#   bash scripts/up.sh ts         → только TimescaleDB
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
source scripts/lib.sh

docker build -q -t tsdb-gen gen >/dev/null
[ $# -gt 0 ] || set -- ts prom vm
$COMPOSE up -d --wait "$@"
log "подняты: $*"
