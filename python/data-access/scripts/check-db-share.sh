#!/usr/bin/env bash
# Карта разрешения стенда: на каком запросе цену слоя измерять можно.
# Подробности — в bench/check_db_share.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose exec -T -w /bench bench python check_db_share.py
