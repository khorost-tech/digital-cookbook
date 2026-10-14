#!/usr/bin/env bash
# Вынос счётной работы из цикла событий в пул потоков ИЛИ процессов:
# оба исполнителя меряются симметрично, на одной и той же работе.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-3}"

for build in gil nogil; do
  out="fixtures/executor-${build}.txt"
  {
    echo "# run_in_executor: ThreadPoolExecutor и ProcessPoolExecutor, сборка: ${build}"
    echo "# rounds=${ROUNDS}"
    echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "# среда: ${PREFLIGHT}"
    echo "#"
  } > "$out"
  docker compose exec -T bench "/opt/py/${build}/bin/python" \
    run_executor.py "$build" "$ROUNDS" | tr -d '\r' >> "$out"
  echo "готово: $out"
done
