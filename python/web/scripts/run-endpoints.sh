#!/usr/bin/env bash
# Разложение цены фреймворка по трём эндпоинтам. Подробности — в
# bench/run_endpoints.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-3}"
SEED="${SEED:-0}"
out=fixtures/endpoints.txt

{
  echo "# Разложение цены фреймворка по эндпоинтам"
  echo "# rounds=${ROUNDS} seed=${SEED} процессов нагрузчика=4"
  echo "# цель на ядрах 0-1, нагрузчик на 2-15 (bench/pin.py)"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e ROUNDS="$ROUNDS" -e SEED="$SEED" \
  -e OUT=/fixtures/endpoints.txt \
  bench /opt/py/web/bin/python /bench/run_endpoints.py

echo "готово: $out"
