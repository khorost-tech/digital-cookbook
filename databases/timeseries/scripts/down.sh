#!/usr/bin/env bash
# down.sh — остановить стенд и удалить контейнеры вместе с данными.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
docker compose -f compose/compose.yml --profile oss down -v
rm -rf work
