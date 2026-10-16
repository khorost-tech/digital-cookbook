#!/usr/bin/env bash
# Блокирующая проверка равенства результата. Подробности — в
# app/check_equivalence.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose exec -T -w /app bench python check_equivalence.py
