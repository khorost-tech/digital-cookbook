#!/usr/bin/env bash
# Usage: ./run.sh sql/01-scan.sql   → печатает вывод psql (захватывать в fixtures)
set -euo pipefail
docker compose exec -T postgres psql -U postgres -d idxdemo -v ON_ERROR_STOP=1 -f - < "$1"
