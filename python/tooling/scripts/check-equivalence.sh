#!/usr/bin/env bash
# Блокирующая проверка: три инструмента ставят один и тот же набор.
# Подробности — в bench/check_equivalence.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose exec -T -w /bench bench python check_equivalence.py
