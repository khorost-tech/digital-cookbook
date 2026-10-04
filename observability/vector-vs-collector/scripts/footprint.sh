#!/usr/bin/env bash
# Замер 7: эксплуатационная цена — размер образа, объём конфига, валидация.
#
# ⚠ Объём конфига — метрика с оговоркой: он зависит от того, кто и как писал
# конфиг. Число публикуется ВМЕСТЕ с самими конфигами, чтобы читатель судил
# сам, а не вместо них. Комментарии из подсчёта исключены: иначе мерялась бы
# словоохотливость автора стенда.
#
# Размер образа и наличие валидатора — величины объективные.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

OUT="results/09-footprint.txt"

img_size_mb() {
  docker image inspect -f '{{.Size}}' "$1" 2>/dev/null | awk '{printf "%.0f", $1/1024/1024}'
}

# Непустые строки без комментариев.
cfg_lines() {
  grep -vE '^\s*(#|$)' "$@" 2>/dev/null | wc -l | tr -d ' '
}

{
  echo "Замер 7: эксплуатационная цена трёх конвейеров"
  echo "Дата: 2026-07-28"
  echo
  echo "── Размер образа ─────────────────────────────────────────────"
  printf "  %-46s %s МБ\n" "timberio/vector:0.57.0-debian" "$(img_size_mb timberio/vector:0.57.0-debian)"
  printf "  %-46s %s МБ\n" "otel/opentelemetry-collector-contrib:0.157.0" "$(img_size_mb otel/opentelemetry-collector-contrib:0.157.0)"
  printf "  %-46s %s МБ\n" "fluent/fluent-bit:5.0.9" "$(img_size_mb fluent/fluent-bit:5.0.9)"
  echo
  echo "  Оговорка: у Vector взят вариант -debian (с shell внутри), он заведомо"
  echo "  больше distroless-варианта. Образы Collector и Fluent Bit — distroless,"
  echo "  внутрь зайти нельзя: docker run --entrypoint sh даёт"
  echo "  exec: \"sh\": executable file not found in \$PATH."
  echo
  echo "── Пользователь по умолчанию ─────────────────────────────────"
  for img in timberio/vector:0.57.0-debian otel/opentelemetry-collector-contrib:0.157.0 fluent/fluent-bit:5.0.9; do
    printf "  %-46s %s\n" "$img" "$(docker image inspect -f '{{if .Config.User}}{{.Config.User}}{{else}}(не задан, то есть root){{end}}' "$img" 2>/dev/null)"
  done
  echo
  echo "── Объём конфига под одну и ту же задачу (строк без комментариев) ──"
  printf "  %-46s %s\n" "vector/agent.yaml" "$(cfg_lines vector/agent.yaml)"
  printf "  %-46s %s\n" "otelcol/config.yaml" "$(cfg_lines otelcol/config.yaml)"
  printf "  %-46s %s\n" "fluentbit/fluent-bit.yaml + parsers.yaml" "$(cfg_lines fluentbit/fluent-bit.yaml fluentbit/parsers.yaml)"
  echo
  echo "── Валидация конфига ─────────────────────────────────────────"
} > "$OUT"

# Ненулевой код здесь — ИСКОМЫЙ результат, а не сбой: команды обёрнуты в
# «|| rc=$?», иначе set -e обрывает скрипт на первой же найденной ошибке,
# то есть замер умения находить ошибки падал бы ровно при их нахождении.
rc_vector=0; rc_otel=0; rc_flb=0

# Проверяем не «есть ли подкоманда», а ЛОВИТ ЛИ она заведомо сломанный конфиг.
# Наличие команды без способности найти ошибку бесполезно для CI.
# Каталог для заведомо сломанных конфигов — ВНУТРИ стенда, а не в /tmp.
# В Git Bash на Windows /tmp не совпадает с тем, что видит docker:
# монтирование отдаёт пустой каталог, файлы не находятся, и все три
# проверки вернули бы ненулевой код от «файл не найден». Проверка бы
# «прошла», не проверив ничего.
BROKEN="$PWD/.tmp-broken"
mkdir -p "$BROKEN"
printf 'sources:\n  bad:\n    type: no_such_source_type\n' > "$BROKEN"/vector.yaml
printf 'receivers:\n  no_such_receiver:\nservice:\n  pipelines:\n    logs:\n      receivers: [no_such_receiver]\n      exporters: [no_such_exporter]\n' > "$BROKEN"/otelcol.yaml
printf 'pipeline:\n  inputs:\n    - name: no_such_input_plugin\n' > "$BROKEN"/fluent-bit.yaml

docker run --rm -v "$BROKEN:/cfg:ro" timberio/vector:0.57.0-debian \
  validate /cfg/vector.yaml >/dev/null 2>&1 || rc_vector=$?
docker run --rm -v "$BROKEN:/cfg:ro" otel/opentelemetry-collector-contrib:0.157.0 \
  validate --config /cfg/otelcol.yaml >/dev/null 2>&1 || rc_otel=$?
# У Fluent Bit подкоманды валидации нет — единственная проверка это старт,
# поэтому запускаем с ограничением по времени и смотрим, упал ли процесс.
timeout 15 docker run --rm -v "$BROKEN:/cfg:ro" fluent/fluent-bit:5.0.9 \
  --config=/cfg/fluent-bit.yaml >/dev/null 2>&1 || rc_flb=$?

{
  printf "  %-24s %s\n" "vector validate"   "код возврата $rc_vector на заведомо сломанном конфиге"
  printf "  %-24s %s\n" "otelcol validate"  "код возврата $rc_otel на заведомо сломанном конфиге"
  printf "  %-24s %s\n" "fluent-bit (старт)" "код возврата $rc_flb — отдельной подкоманды валидации НЕТ"
  echo
  echo "  Ненулевой код = ошибка найдена. Для CI важно именно это, а не наличие"
  echo "  команды: у Fluent Bit проверить конфиг можно только запуском."
} >> "$OUT"

rm -rf "$BROKEN"
cat "$OUT"
