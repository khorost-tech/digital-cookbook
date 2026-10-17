#!/usr/bin/env bash
# Сборка локального индекса. Делается один раз перед замерами.
#
# Индекс НЕ хранится в репозитории: это полсотни мегабайт колёс, которые
# устареют быстрее самого стенда. Скрипт восстанавливает его по
# requirements.txt подопытного проекта.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose run --rm --no-deps -w /bench bench \
  python build_index.py /app/requirements.txt

echo "индекс собран; поднять раздачу: docker compose up -d index"
