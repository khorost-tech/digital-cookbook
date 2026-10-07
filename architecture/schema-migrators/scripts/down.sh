#!/usr/bin/env bash
# down.sh — снести всё вместе с томами и сетью.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
for c in pg crdb pico; do docker compose -f "compose/$c.yml" down -v >/dev/null 2>&1; done
docker network rm smig >/dev/null 2>&1 || true
echo "[down] снесено"
