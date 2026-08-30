#!/usr/bin/env bash
set -euo pipefail
docker compose exec -T postgres psql -U postgres -d waldemo -v ON_ERROR_STOP=1 -f - < 01-wal-growth.sql
