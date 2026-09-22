#!/usr/bin/env bash
# Замеры 1 и 2: ресурсы конвейера и цена трансформации.
# Использование: ./scripts/bench.sh <vector|otelcol|fluentbit> <full|passthrough> <rate>
#
# Прогон ВСЕГДА одиночный: поднимается ровно один конвейер. Одновременный
# запуск нескольких превратил бы замер CPU в конкуренцию за ядра.
set -euo pipefail

PIPELINE="${1:-vector}"
VARIANT="${2:-full}"
RATE="${3:-5000}"
DURATION=60

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

AUTH="admin:VectorDemo#2026"

case "$PIPELINE" in
  vector)
    CONTAINER=vvc-vector
    [[ "$VARIANT" == "full" ]] && CFG=/etc/vector/agent.yaml || CFG=/etc/vector/passthrough.yaml
    export VECTOR_CONFIG="$CFG"
    [[ "$VARIANT" == "full" ]] && INDEX=logs-vector || INDEX=logs-vector-passthrough
    STATE_VOLUME=vector-vs-collector_vector-data
    ;;
  otelcol)
    CONTAINER=vvc-otelcol
    [[ "$VARIANT" == "full" ]] && CFG=/etc/otelcol/config.yaml || CFG=/etc/otelcol/passthrough.yaml
    export OTELCOL_CONFIG="$CFG"
    [[ "$VARIANT" == "full" ]] && INDEX=logs-otelcol || INDEX=logs-otelcol-passthrough
    STATE_VOLUME=vector-vs-collector_otelcol-data
    ;;
  fluentbit)
    CONTAINER=vvc-fluentbit
    [[ "$VARIANT" == "full" ]] && CFG=/etc/fluent-bit/fluent-bit.yaml || CFG=/etc/fluent-bit/passthrough.yaml
    export FLUENTBIT_CONFIG="$CFG"
    [[ "$VARIANT" == "full" ]] && INDEX=logs-fluentbit || INDEX=logs-fluentbit-passthrough
    STATE_VOLUME=vector-vs-collector_fluentbit-data
    ;;
  *) echo "конвейер: vector | otelcol | fluentbit" >&2; exit 2 ;;
esac
case "$VARIANT" in full|passthrough) ;; *) echo "вариант: full | passthrough" >&2; exit 2 ;; esac

RUN="results/bench-${PIPELINE}-${VARIANT}-${RATE}"
N=1
while [[ -e "${RUN}-r${N}.csv" ]]; do N=$((N+1)); done
OUT="${RUN}-r${N}.csv"

# ── Подготовка: чистое состояние конвейера И чистый индекс ────────────────
# Оба обязательны. Живьём: том чистили, индекс нет — счётчик показал ровно
# тройное ожидание (54801 против 18267), и это выглядело как дублирование
# событий инструментом, хотя было просто накоплением трёх прогонов.
# Отдельно: `docker volume rm` на томе ОСТАНОВЛЕННОГО, но существующего
# контейнера молча не срабатывает — нужен rm -sf самого сервиса.
# Порядок здесь важен, и он выведен из ошибки. Сначала было: поднять конвейер,
# потом чистить. Получилось, что конвейер стартовал на файлах ПРЕДЫДУЩЕГО
# прогона и дочитывал их ровно во время idle-фазы — замер показал idle 89.3%
# CPU против 10.4% под нагрузкой, а в индекс попало 667086 событий при
# ожидании 275141. Правильный порядок: погасить конвейер → убрать его
# состояние → стереть файлы логов → удалить индекс → и только потом поднять
# конвейер, которому нечего читать.
docker compose rm -sf "$PIPELINE" >/dev/null 2>&1 || true
docker volume rm "$STATE_VOLUME" >/dev/null 2>&1 || true

# Инфраструктура без конвейера: нужен живой OpenSearch и доступ к тому logs.
docker compose up -d opensearch prometheus >/dev/null

docker compose run --rm --entrypoint sh loadgen \
  -c 'rm -f /logs/*.log /logs/manifest.json' >/dev/null 2>&1 || true

docker compose exec -T opensearch curl -sk --connect-timeout 5 --max-time 20 \
  -u "$AUTH" -X DELETE "https://localhost:9200/${INDEX}" >/dev/null 2>&1 || true

./scripts/up.sh "$PIPELINE" >/dev/null

# ── Съём метрик ──────────────────────────────────────────────────────────
# Снимаем ПОТОКОВЫМ docker stats (без --no-stream). Разовые снимки на соседнем
# стенде дали расхождение до 5.8x при формально одинаковых параметрах: сам
# вызов `docker stats --no-stream` занимает ~2.2 с, а нагрузка идёт скачками
# (генератор копит запись в буфере 1 МиБ и сбрасывает пачками), поэтому редкие
# снимки то попадают на всплеск, то нет.
#
# Про дубли кадров: docker stats перерисовывает одно обновление дважды.
# Дедупликация НЕ делается сознательно — на idle многие подряд идущие
# одинаковые кадры законны (реальный простой), и отбрасывание "совпадает с
# предыдущим" убрало бы настоящие данные. Поэтому "по N кадрам" ниже — число
# сырых кадров, а не независимых измерений.
sample_stream() {
  local phase="$1" seconds="$2" raw
  raw="$(mktemp)"
  docker stats --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' "$CONTAINER" > "$raw" 2>&1 &
  local pid=$!
  sleep "$seconds"
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  sed 's/\x1b\[[0-9;]*[A-Za-z]//g' "$raw" | grep -aE "^${CONTAINER}," \
    | sed 's/%//; s/MiB.*//; s|/.*||' \
    | awk -F, -v p="$phase" '{printf "%s,%s,%s,%s\n", p, $1, $2, $3}' >> "$OUT"
  rm -f "$raw"

  local got
  got=$(awk -F, -v p="$phase" '$1==p' "$OUT" | wc -l)
  if (( got < 4 )); then
    echo "снято подозрительно мало метрик на фазе $phase ($got строк) — docker stats не сработал?" >&2
    exit 1
  fi
  # Страховка от застрявшего потока — только на фазе load: под нагрузкой CPU
  # реально скачет, поэтому одно и то же значение все кадры означает сбой.
  # На idle такая проверка была бы ложной тревогой: 0.00% час за часом там
  # законное показание.
  if [[ "$phase" == "load" ]]; then
    local distinct
    distinct=$(awk -F, -v p="$phase" '$1==p {print $3}' "$OUT" | sort -u | wc -l)
    if (( distinct < 2 )); then
      echo "на фазе load CPU одинаков во всех кадрах ($distinct значение) — docker stats завис?" >&2
      exit 1
    fi
  fi
}

echo "phase,container,cpu_pct,mem_mb" > "$OUT"

sample_stream idle 10

# -d: код возврата подтверждает только СТАРТ контейнера генератора. Полноту
# прогона подтверждает сверка gen_total с rate*duration ниже.
if ! docker compose run -d --rm loadgen -rate "$RATE" -duration "${DURATION}s" >/dev/null; then
  echo "не запустить генератор" >&2
  exit 1
fi

sample_stream load "$DURATION"

sleep 40   # дать конвейеру слить хвост

# Выборки парсятся в предположении, что память в MiB: гигабайтные значения
# посчитались бы как мегабайты и дали бы молча неверные числа.
if grep -q "GiB" "$OUT"; then
  echo "ПРОВАЛ: память контейнера превысила 1 GiB — парсер выборок неверен, править bench.sh" >&2
  exit 1
fi

if ! manifest="$(docker compose run --rm --entrypoint sh loadgen -c 'cat /logs/manifest.json')"; then
  echo "не прочитать манифест генератора" >&2
  exit 1
fi
mfield() { printf '%s' "$manifest" | tr -d '\r\n' | sed "s/.*\"$1\":\([0-9]*\).*/\1/"; }
gen_total="$(mfield total)"
gen_app="$(mfield app)"
nginx_expected="$(mfield nginx_expected)"
for v in "$gen_total" "$gen_app" "$nginx_expected"; do
  if ! [[ "$v" =~ ^[0-9]+$ ]]; then
    echo "манифест генератора неполон: $manifest" >&2
    exit 1
  fi
done

# manifest.json перезаписывается только в САМОМ КОНЦЕ прогона генератора:
# если контейнер стартовал, а процесс внутри упал, на томе остался бы манифест
# от предыдущего запуска и был бы молча принят за текущий. Допуск 2% отделяет
# дрожание тикера (доли процента) от настоящей деградации.
expected_total=$(( RATE * DURATION ))
tolerance=$(( expected_total * 2 / 100 ))
diff=$(( gen_total - expected_total ))
(( diff < 0 )) && diff=$(( -diff ))
if (( diff > tolerance )); then
  echo "манифест не от этого прогона: total=$gen_total, ожидалось $expected_total (допуск $tolerance)" >&2
  exit 1
fi

# Ожидание. Дедупликации в этом стенде нет ни у одного конвейера, поэтому
# app-события доезжают ВСЕ; поле app_expected из манифеста здесь неприменимо —
# оно про дедуп соседнего стенда.
if [[ "$VARIANT" == "passthrough" ]]; then
  expected="$gen_total"          # ни парсинга, ни фильтрации — доезжает всё
else
  expected=$(( nginx_expected + gen_app ))
fi

# Ждём стабилизации счётчика: индексация асинхронна. Одиночный сбой чтения —
# повод повторить попытку, а не объявить бэкенд недоступным.
prev=-1; stable=0; indexed=""; read_ok=0
for _ in $(seq 1 30); do
  cur="$(docker compose exec -T opensearch curl -sk --connect-timeout 3 --max-time 10 \
        -u "$AUTH" "https://localhost:9200/${INDEX}/_count" 2>/dev/null \
        | sed 's/.*"count":\([0-9]*\).*/\1/')"
  if [[ "$cur" =~ ^[0-9]+$ ]]; then
    read_ok=1
    indexed="$cur"
    if [[ "$cur" == "$prev" ]]; then
      stable=$((stable+1))
      (( stable >= 2 )) && break
    else
      stable=0
    fi
    prev="$cur"
  fi
  sleep 3
done
if (( read_ok == 0 )); then
  echo "не прочитать _count индекса $INDEX: OpenSearch недоступен?" >&2
  exit 1
fi

{
  echo "=== $PIPELINE / $VARIANT / rate=$RATE / прогон r$N"
  echo "сгенерировано: $gen_total"
  echo "ожидалось:     $expected"
  echo "в индексе:     $indexed"
  echo "недоставлено:  $(( expected - indexed ))"
  awk -F, '$1=="load" {c+=$3; m+=$4; n++} END {
    if (n) printf "под нагрузкой: CPU %.1f%%, RAM %.0f MiB (по %d кадрам)\n", c/n, m/n, n }' "$OUT"
  awk -F, '$1=="idle" {c+=$3; m+=$4; n++} END {
    if (n) printf "idle:          CPU %.1f%%, RAM %.0f MiB\n", c/n, m/n }' "$OUT"
  echo
} >> results/bench-summary.txt

tail -9 results/bench-summary.txt
