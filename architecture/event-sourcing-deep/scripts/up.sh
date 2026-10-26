#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

log "поднимаем стенд…"
docker compose -f "$COMPOSE" up -d
wait_healthy "$PG_CONTAINER" 60
wait_healthy esd-kurrentdb 120
wait_healthy esd-kafka 120

log "накатываем схему…"
docker exec -i "$PG_CONTAINER" psql -U esdemo -d esdemo -v ON_ERROR_STOP=1 < sql/01-schema.sql
log "готово"
