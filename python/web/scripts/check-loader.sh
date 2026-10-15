#!/usr/bin/env bash
# Не является ли нагрузчик узким местом. Подробности — в bench/check_loader.py.
set -euo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
bash "$(dirname "$0")/preflight.sh"
docker compose exec -T bench /opt/py/web/bin/python /bench/check_loader.py
