#!/usr/bin/env bash
# Сколько даёт устранение вызова мелкой функции — без профайлера.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ITERS="${ITERS:-300}"
ROUNDS="${ROUNDS:-7}"
out=fixtures/inline.txt

{
  echo "# Устранение вызова мелкой частой функции, сборка: gil"
  echo "# iters=${ITERS} rounds=${ROUNDS}"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
} > "$out"

docker compose exec -T bench /opt/py/gil/bin/python inline_compare.py "$ITERS" "$ROUNDS" | tr -d '\r' >> "$out"

echo "готово: $out"
