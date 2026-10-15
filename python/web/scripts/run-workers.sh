#!/usr/bin/env bash
# Воркеры: сколько их нужно и почему ответ зависит от типа нагрузки.
# Подробности — в bench/run_workers.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-5}"
SEED="${SEED:-0}"
out=fixtures/workers.txt

{
  echo "# Воркеры: uvicorn-asgi, gunicorn-sync, gunicorn-gthread"
  echo "# rounds=${ROUNDS} seed=${SEED} одновременность=64 процессов нагрузчика=4"
  echo "# цель на ядрах 0-7, нагрузчик на 8-15 (bench/pin.py)"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e ROUNDS="$ROUNDS" -e SEED="$SEED" \
  -e OUT=/fixtures/workers.txt \
  bench /opt/py/web/bin/python /bench/run_workers.py

echo "готово: $out"
