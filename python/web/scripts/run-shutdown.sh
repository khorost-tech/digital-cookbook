#!/usr/bin/env bash
# Корректное завершение как измеряемый сценарий. Подробности — в
# bench/run_shutdown.py, включая объяснение, зачем нужен контроль SIGKILL.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-3}"
out=fixtures/shutdown.txt

{
  echo "# Завершение под нагрузкой: SIGTERM против SIGKILL как контроля"
  echo "# rounds=${ROUNDS} одновременность=32, нагрузка на /wait"
  echo "# цель на ядрах 0-3, нагрузчик на 4-15"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e ROUNDS="$ROUNDS" -e OUT=/fixtures/shutdown.txt \
  bench /opt/py/web/bin/python /bench/run_shutdown.py

echo "готово: $out"
