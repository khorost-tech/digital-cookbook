#!/usr/bin/env bash
# Usage: ./run.sh sql/01-connection-cost.sql   → psql НАПРЯМУЮ к postgres (для наблюдения).
# Демонстрации через пулеры ходят на порты 6432/6433/6434 (см. сценарии и bench.sh).
set -euo pipefail
docker compose exec -T postgres psql -U postgres -d opsdemo -v ON_ERROR_STOP=1 -f - < "$1"
