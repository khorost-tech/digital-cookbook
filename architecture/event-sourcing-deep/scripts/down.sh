#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

log "останавливаем стенд и удаляем тома…"
docker compose -f "$COMPOSE" down -v --remove-orphans
