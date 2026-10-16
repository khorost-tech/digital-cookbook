#!/usr/bin/env bash
# Разложение цены доступа по слоям. Подробности — в bench/run_layers.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-11}"
SEED="${SEED:-0}"
out=fixtures/layers.txt

{
  echo "# Разложение цены доступа к данным по слоям"
  echo "# rounds=${ROUNDS} seed=${SEED}, порядок плеч по латинскому квадрату"
  echo "# разогрев 20 с в начале каждого круга, результат отбрасывается"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e ROUNDS="$ROUNDS" -e SEED="$SEED" \
  -e OUT=/fixtures/layers.txt -e PYTHONPATH=/app:/bench \
  -w /bench bench python run_layers.py

echo "готово: $out"
