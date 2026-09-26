#!/usr/bin/env bash
# Usage: ./run.sh sql/01-partitioning.sql   → печатает вывод psql (захватывать в fixtures)
set -euo pipefail
docker compose exec -T postgres psql -U postgres -d opsdemo -v ON_ERROR_STOP=1 -f - < "$1"
