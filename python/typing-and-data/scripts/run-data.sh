#!/usr/bin/env bash
# Чтение Parquet: кто не полез в разделы, не нужные ответу.
# Подробности — в bench/run_data.py.
#
# Набор данных живёт ВНУТРИ контейнера (/data), а не на смонтированном
# томе: сценарию нужно снимать и возвращать права доступа, а на bind-mount
# из Windows они не действуют. Набор порождается арифметикой без
# случайности, поэтому хранить его негде — он воспроизводится точь-в-точь.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

out=fixtures/data.txt

{
  echo "# Чтение Parquet: pandas и polars, пять способов задать один вопрос"
  echo "# состояния набора: all-readable (контроль) и pruned-locked"
  echo "# лишние разделы делаются НЕЧИТАЕМЫМИ — дошедший до ответа доказал,"
  echo "#   что в них не заглядывал; байты для этого не годятся, см. arm_data.py"
  echo "# плечи выполняются от непривилегированного пользователя"
  echo "# времени этот стенд не меряет"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "#"
} > "$out"

docker compose build bench >/dev/null
docker compose run --rm -e OUT=/fixtures/data.txt -e DATASET=/data bench \
  python /bench/run_data.py

echo "готово: $out"

PY="$(command -v python3 || command -v python)"
# Разбор — прямым вызовом, без конвейера: `script | tail` в этой серии
# дважды съедал код возврата.
"$PY" scripts/analyze-data.py
