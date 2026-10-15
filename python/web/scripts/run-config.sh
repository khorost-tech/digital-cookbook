#!/usr/bin/env bash
# Отказ на старте при неполной конфигурации. Подробности — в
# bench/run_config.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-3}"
out=fixtures/config.txt

{
  echo "# Конфигурация: наивное чтение против проверки на старте"
  echo "# rounds=${ROUNDS}, четыре конфигурации: ok, missing, bad_url, bad_int"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e ROUNDS="$ROUNDS" -e OUT=/fixtures/config.txt \
  bench /opt/py/web/bin/python /bench/run_config.py

echo "готово: $out"
