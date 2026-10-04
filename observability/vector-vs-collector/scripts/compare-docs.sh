#!/usr/bin/env bash
# Критерий эквивалентности конвейеров.
#
# Замеры CPU имеют смысл только если конвейеры делают ОДИНАКОВУЮ работу.
# Проверяется это по выходному документу: набор ПРИКЛАДНЫХ полей обязан
# совпасть у всех трёх. Расхождение — стоп-сигнал, числа публиковать нельзя.
#
# Служебные поля из сравнения исключаются осознанно. У каждого инструмента
# своя модель записи (у Vector четыре плоских поля, у Collector обёртка OTel,
# у Fluent Bit одно @timestamp), и требовать их совпадения значило бы требовать,
# чтобы инструменты перестали быть собой. Разница моделей — предмет статьи,
# а не дефект стенда.
#
# nginx- и app-события сверяются ОТДЕЛЬНО: наборы полей у них разные по
# построению, и слияние в одно множество спрятало бы расхождение.
#
# К OpenSearch обращаемся ИЗНУТРИ docker-сети, а не через проброшенный порт:
# на машине сборки проброс портов Docker Desktop не работает (см. results/01),
# и это к тому же снимает зависимость от self-signed TLS на хосте.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

OUT="results/04-equivalence.txt"
AUTH="admin:VectorDemo#2026"

# Служебные поля, исключаемые из сверки, по инструментам.
declare -A SERVICE_FIELDS=(
  [logs-vector]="file host source_type timestamp"
  [logs-otelcol]="data_stream log.file.name"
  [logs-fluentbit]="@timestamp"
)

# Достаёт отсортированный список прикладных полей одного документа.
# У Collector прикладные поля лежат внутри attributes — разворачиваем.
keys_of() {   # keys_of <index> <service>
  local index="$1" svc="$2" field="service" exclude="${SERVICE_FIELDS[$1]}"
  [[ "$index" == "logs-otelcol" ]] && field="attributes.service"
  docker compose exec -T opensearch curl -sk --connect-timeout 5 --max-time 20 \
    -u "$AUTH" "https://localhost:9200/${index}/_search?size=1&q=${field}:${svc}" 2>/dev/null \
  | python -c "
import json,sys
try:
    hits = json.load(sys.stdin)['hits']['hits']
except Exception:
    sys.exit(0)
if not hits:
    sys.exit(0)
src = hits[0]['_source']
if 'attributes' in src and isinstance(src['attributes'], dict):
    src = src['attributes']
skip = set('''$exclude'''.split())
print(' '.join(sorted(k for k in src if k not in skip)))
"
}

fail=0
: > "$OUT"
{
  echo "Критерий эквивалентности конвейеров"
  echo "Дата прогона: $(docker compose exec -T opensearch date -u '+%Y-%m-%d %H:%M:%S UTC' 2>/dev/null | tr -d '\r')"
  echo
} >> "$OUT"

for kind in nginx api; do
  echo "=== прикладные поля документа, service=$kind" | tee -a "$OUT"
  ref=""
  for idx in logs-vector logs-otelcol logs-fluentbit; do
    got="$(keys_of "$idx" "$kind")"
    if [[ -z "$got" ]]; then
      echo "  $idx: НЕТ ДОКУМЕНТОВ (конвейер не прогонялся или потерял поток)" | tee -a "$OUT"
      fail=1
      continue
    fi
    echo "  $idx: $got" | tee -a "$OUT"
    if [[ -z "$ref" ]]; then
      ref="$got"
    elif [[ "$got" != "$ref" ]]; then
      echo "    РАСХОЖДЕНИЕ с эталоном logs-vector" | tee -a "$OUT"
      fail=1
    fi
  done
  echo | tee -a "$OUT"
done

if (( fail )); then
  echo "ПРОВАЛ: конвейеры делают разную работу, замеры сравнивать нельзя" | tee -a "$OUT" >&2
  exit 1
fi

echo "OK: наборы прикладных полей совпадают, замеры сопоставимы" | tee -a "$OUT"
