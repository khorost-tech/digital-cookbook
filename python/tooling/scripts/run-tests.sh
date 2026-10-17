#!/usr/bin/env bash
# Тесты: моки против фейка против настоящей базы.
# Подробности — в bench/run_tests.py.
#
# ЗАПУСК ОТДЕЛЬНЫМ КОНТЕЙНЕРОМ, а не через docker compose exec: набору
# нужен доступ к демону Docker, чтобы Testcontainers поднимал соседние
# контейнеры. Сокет монтируется внутрь, а TESTCONTAINERS_HOST_OVERRIDE
# говорит библиотеке, по какому имени искать опубликованный порт: изнутри
# контейнера localhost — это он сам, а не хост.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

ROUNDS="${ROUNDS:-3}"
SEED="${SEED:-0}"
out=fixtures/tests.txt

{
  echo "# Тесты: mocks, sqlite (фейк), postgres (Testcontainers), hypothesis"
  echo "# состояния кода: none (контроль), null-filter, like-case, group-by, n-plus-1"
  echo "# rounds=${ROUNDS} seed=${SEED}; цена меряется только на исправном коде"
  echo "# property-based: ${HYP_MAX_EXAMPLES:-100} примеров, два прогона —"
  echo "#   без сжатия (счёт примеров) и со сжатием (контрпример)"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
} > "$out"

docker compose build bench >/dev/null

docker run --rm \
  -v //var/run/docker.sock:/var/run/docker.sock \
  --add-host=host.docker.internal:host-gateway \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
  -e ROUNDS="$ROUNDS" -e SEED="$SEED" -e OUT=/fixtures/tests.txt \
  -e HYP_MAX_EXAMPLES="${HYP_MAX_EXAMPLES:-100}" \
  -v "$(pwd)/index/packages:/wheels:ro" \
  -v "$(pwd)/tests:/work/tests" \
  -v "$(pwd)/bench:/bench" \
  -v "$(pwd)/fixtures:/fixtures" \
  -v python-tooling_venvs:/venvs \
  -w /bench \
  python-tooling-bench python run_tests.py

echo "готово: $out"

PY="$(command -v python3 || command -v python)"
# Прямой вызов, без конвейера: `script | tail` в этой серии дважды
# съедал код возврата.
"$PY" scripts/analyze-tests.py
