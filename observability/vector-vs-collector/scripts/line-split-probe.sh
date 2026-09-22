#!/usr/bin/env bash
# Контрольный опыт: рвёт ли Collector строки активно пишущегося файла.
#
# Замер ресурсов показал у Collector СИСТЕМАТИЧЕСКИЙ избыток документов в
# индексе (+27 и +26 на двух прогонах) при нулевом расхождении у Vector и
# Fluent Bit на том же входе. Объяснение — force_flush_period ресивера
# file_log: по умолчанию 500 мс, и незавершённая строка уходит как
# самостоятельная запись.
#
# Опыт считает ДВА независимых числа в каждом прогоне:
#   1) расхождение счётчиков: сколько документов сверх ожидаемого;
#   2) число документов БЕЗ поля client, то есть не прошедших regex, — это
#      и есть обрывки.
# Если объяснение верное, числа обязаны совпасть: каждая разорванная строка
# даёт два обрывка вместо одной целой записи, то есть +1 документ и +2
# нераспарсенных. Проверяется соотношение обрывки = 2 × расхождение.
#
# Прогоняется дважды: с умолчанием и с force_flush_period: 0.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
AUTH="admin:VectorDemo#2026"
RATE="${1:-5000}"
DURATION=60
OUT="results/10-line-split-probe.txt"

os_curl() {
  docker compose exec -T opensearch curl -sk --connect-timeout 5 --max-time 30 -u "$AUTH" "$@" 2>/dev/null
}

count_of() {   # count_of <query-string или пусто>
  local q="${1:-}"
  local url="https://localhost:9200/logs-otelcol/_count"
  [[ -n "$q" ]] && url="${url}?q=${q}"
  os_curl "$url" | sed 's/.*"count":\([0-9]*\).*/\1/'
}

run_variant() {   # run_variant <flush-default|flush-zero> <config>
  local label="$1" cfg="$2"

  docker compose rm -sf otelcol >/dev/null 2>&1 || true
  docker volume rm vector-vs-collector_otelcol-data >/dev/null 2>&1 || true
  docker compose up -d opensearch prometheus >/dev/null
  docker compose run --rm --entrypoint sh loadgen -c 'rm -f /logs/*.log /logs/manifest.json' >/dev/null 2>&1 || true
  os_curl -X DELETE "https://localhost:9200/logs-otelcol" >/dev/null 2>&1 || true

  OTELCOL_CONFIG="$cfg" ./scripts/up.sh otelcol >/dev/null
  docker compose run --rm loadgen -rate "$RATE" -duration "${DURATION}s" >/dev/null 2>&1

  # ждём стабилизации счётчика
  local prev=-1 stable=0 cur=""
  for _ in $(seq 1 40); do
    cur="$(count_of)"
    if [[ "$cur" =~ ^[0-9]+$ ]]; then
      if [[ "$cur" == "$prev" ]]; then
        stable=$((stable+1)); (( stable >= 3 )) && break
      else
        stable=0
      fi
      prev="$cur"
    fi
    sleep 3
  done
  local indexed="$cur"

  local manifest gen_app nginx_expected expected
  manifest="$(docker compose run --rm --entrypoint sh loadgen -c 'cat /logs/manifest.json')"
  gen_app="$(printf '%s' "$manifest" | tr -d '\r\n' | sed 's/.*"app":\([0-9]*\).*/\1/')"
  nginx_expected="$(printf '%s' "$manifest" | tr -d '\r\n' | sed 's/.*"nginx_expected":\([0-9]*\).*/\1/')"
  expected=$(( nginx_expected + gen_app ))

  # Обрывки считаются в ОБЕИХ ветках, и это не педантизм: первая редакция
  # опыта смотрела только nginx-ветку, получила 26 обрывков против расхождения
  # 27 и объявила объяснение неполным. Недостающее было в app-ветке.
  #
  # Ищем по отсутствию ПОЛЯ, а не по длине строки: длина зависит от того, в
  # каком месте разрезало, и порог по ней был бы подгонкой под ответ.
  #
  # Обрывок nginx: regex не совпал, поля client нет. Поле service при этом
  # ЕСТЬ — его ставит transform безусловно, независимо от разбора.
  # Обрывок app: json_parser не разобрал, поля service нет вовсе.
  local unparsed_nginx unparsed_app unparsed
  unparsed_nginx="$(os_curl -H 'Content-Type: application/json' -X POST \
    "https://localhost:9200/logs-otelcol/_count" \
    -d '{"query":{"bool":{"must":[{"term":{"attributes.service":"nginx"}}],"must_not":[{"exists":{"field":"attributes.client"}}]}}}' \
    | sed 's/.*"count":\([0-9]*\).*/\1/')"
  unparsed_app="$(os_curl -H 'Content-Type: application/json' -X POST \
    "https://localhost:9200/logs-otelcol/_count" \
    -d '{"query":{"bool":{"must_not":[{"exists":{"field":"attributes.service"}}]}}}' \
    | sed 's/.*"count":\([0-9]*\).*/\1/')"
  unparsed=$(( unparsed_nginx + unparsed_app ))

  local delta=$(( indexed - expected ))
  {
    echo "── вариант: $label   (конфиг $cfg)"
    echo "   ожидалось:                       $expected"
    echo "   в индексе:                       $indexed"
    echo "   расхождение:                     $delta"
    echo "   обрывков в nginx-ветке:          $unparsed_nginx  (есть service, нет client)"
    echo "   обрывков в app-ветке:            $unparsed_app  (нет service вовсе)"
    echo "   обрывков всего:                  $unparsed"
    if (( delta > 0 )); then
      # Арифметика: разорванная строка даёт ДВА документа вместо одного,
      # то есть +1 к расхождению и +2 к числу обрывков. Значит обрывков
      # должно быть ровно вдвое больше расхождения.
      echo "   проверка соотношения:            обрывков $unparsed против 2 × $delta = $(( delta * 2 ))"
      if (( unparsed == delta * 2 )); then
        echo "   → СОВПАЛО: каждая разорванная строка дала ровно два обрывка,"
        echo "     расхождение объясняется разрезанием строк полностью"
      else
        echo "   → НЕ совпало: объяснение неполное, есть ещё один источник расхождения"
      fi
    else
      echo "   → расхождения нет"
      if (( unparsed != 0 )); then
        echo "   ⚠ но нераспарсенных документов $unparsed — разобраться отдельно"
      fi
    fi
    echo
  } >> "$OUT"
}

{
  echo "Контрольный опыт: разрезает ли Collector строки активно пишущегося файла"
  echo "Дата: $(docker compose exec -T opensearch date -u '+%Y-%m-%d %H:%M:%S UTC' 2>/dev/null | tr -d '\r')"
  echo "Параметры: rate=$RATE, длительность ${DURATION}s, образ otel/opentelemetry-collector-contrib:0.157.0"
  echo
} > "$OUT"

run_variant "force_flush_period по умолчанию (500 мс)" /etc/otelcol/flush-default.yaml
run_variant "force_flush_period: 0"                    /etc/otelcol/config.yaml

cat "$OUT"
