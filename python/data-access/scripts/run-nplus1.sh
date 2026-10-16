#!/usr/bin/env bash
# N+1 и стратегии загрузки: счёт запросов на стороне базы.
# Подробности — в bench/run_nplus1.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ROUNDS="${ROUNDS:-5}"
out=fixtures/nplus1.txt

{
  echo "# N+1 и стратегии загрузки связей"
  echo "# rounds=${ROUNDS}, счёт запросов по pg_stat_statements"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e ROUNDS="$ROUNDS" -e OUT=/fixtures/nplus1.txt \
  -e PYTHONPATH=/app:/bench -w /bench bench python run_nplus1.py

echo "готово: $out"

{
  echo "# Ленивая загрузка в async-сессии: фиксация поведения"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
} > fixtures/lazy-async.txt

docker compose exec -T -e OUT=/fixtures/lazy-async.txt \
  -e PYTHONPATH=/app:/bench -w /bench bench python run_lazy_async.py

echo "готово: fixtures/lazy-async.txt"
