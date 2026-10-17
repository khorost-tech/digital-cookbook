#!/usr/bin/env bash
# Образы: шесть стратегий сборки одного приложения.
# Подробности — в bench/run_images.py.
#
# ЭТОТ СЦЕНАРИЙ ИДЁТ НА ХОСТЕ, а не внутри bench-контейнера: ему нужен
# доступ к демону Docker. Остальные сценарии стенда живут в контейнере,
# этот — исключение, и путать их нельзя: docker compose exec тут ни при чём.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

ROUNDS="${ROUNDS:-7}"
SEED="${SEED:-0}"
out=fixtures/images.txt

PY="$(command -v python3 || command -v python)"

{
  echo "# Образы: naive, slim, multistage, uv, uv-compiled, distroless"
  echo "# rounds=${ROUNDS} seed=${SEED}, порядок стратегий по латинскому квадрату"
  echo "# размеры в байтах: сжатый (реестр) и распакованный (диск узла)"
  echo "# перед кругами — пересборка КАЖДОЙ стратегии с --no-cache"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
} > "$out"

ROUNDS="$ROUNDS" SEED="$SEED" OUT="$out" "$PY" bench/run_images.py

echo "готово: $out"

# Разбор — прямым вызовом, без конвейера: `script | tail` в этой серии
# дважды съедал код возврата, и отбитый прогон выглядел успешным.
"$PY" scripts/analyze-images.py
