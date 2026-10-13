#!/usr/bin/env bash
# Матрица типов: кто ловит подсунутый дефект.
# Подробности — в bench/run_typing.py.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

out=fixtures/typing.txt

{
  echo "# Кто ловит дефект: mypy, mypy --strict, pyright, pydantic, выполнение"
  echo "# состояния кода: none (контроль) плюс пять подсунутых дефектов"
  echo "# дефект — правка ИСХОДНИКА, применяется к копии в контейнере"
  echo "# времени этот стенд не меряет: все величины счётные"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
} > "$out"

docker compose build bench >/dev/null
docker compose run --rm -e OUT=/fixtures/typing.txt bench \
  python /bench/run_typing.py

echo "готово: $out"

PY="$(command -v python3 || command -v python)"
# Разбор — прямым вызовом, без конвейера: `script | tail` в этой серии
# дважды съедал код возврата, и отбитый прогон выглядел успешным.
"$PY" scripts/analyze-typing.py
