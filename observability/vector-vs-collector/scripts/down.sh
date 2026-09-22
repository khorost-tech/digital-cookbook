#!/usr/bin/env bash
# Гасит стенд и удаляет тома — следующий замер обязан начинаться с чистого листа.
# Идемпотентен: возвращает 0, даже когда гасить нечего.
#
# Внимание: удаляются и тома состояния конвейеров (чекпойнты чтения файлов),
# и том logs. Если нужно сохранить индексы OpenSearch между прогонами разных
# конвейеров (сверка эквивалентности), гасить надо не этим скриптом, а точечно:
#   docker compose stop vector otelcol fluentbit
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose \
  --profile vector --profile otelcol --profile fluentbit \
  --profile combo --profile otlp --profile gen \
  down -v --remove-orphans
