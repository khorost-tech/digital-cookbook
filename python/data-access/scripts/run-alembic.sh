#!/usr/bin/env bash
# Автогенерация Alembic и проверка отката. Подробности — в
# bench/run_alembic.py.
#
# Сценарий МЕНЯЕТ СХЕМУ и возвращает её обратно сам, а в конце сверяет
# снимок с исходным. Если сверка не сошлась — пересейте базу перед любыми
# другими замерами.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

out=fixtures/alembic.txt

{
  echo "# Автогенерация Alembic: что видит и возвращает ли откат"
  echo "# два режима сравнения, снимок схемы из системного каталога"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T -e OUT=/fixtures/alembic.txt \
  -e PYTHONPATH=/app:/bench -w /bench bench python run_alembic.py

echo "готово: $out"
