#!/usr/bin/env bash
#
# down.sh — снести стенд вместе с томами, включая все оверлеи профилей.
#
# Запуск (из databases/cockroachdb/):  bash scripts/down.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

for f in compose/compose.yml compose/time.yml compose/geo.yml compose/ops.yml; do
    [ -f "$f" ] || continue
    docker compose -f "$f" down -v --remove-orphans >/dev/null 2>&1 || true
done

echo "[down] стенд снесён" >&2
