#!/usr/bin/env bash
# Замер накладных расходов инструментирования: голое приложение против Go SDK и
# Java-агента, и отдельно gRPC против HTTP.
#
#   ./scripts/bench-overhead.sh [запросов] [повторов]
#   ./scripts/bench-overhead.sh --report-only     # пересобрать отчёт из results/.bench
#
# Дисциплина замера (уроки этого и предыдущих стендов):
#
#  1. Между режимами стенд поднимается ЗАНОВО с чистыми томами: остаточные данные
#     в Tempo и Loki меняют поведение бэкендов и портят числа.
#  2. Перед каждым замером — прогрев и пауза: у JVM первые запросы медленнее в
#     разы (JIT, пул соединений), без прогрева измеряется разогрев, а не работа.
#  3. Ресурсы снимаются ЦИКЛОМ `docker stats --no-stream`, а НЕ потоковым
#     `docker stats`. Причина фактическая: в потоковом режиме под нагрузкой этот
#     стенд получал `-- / --` в 1468 строках из 1508 (97%), то есть годных точек
#     оставалось около сорока, а первая редакция парсера считала прочерк нулём и
#     выдавала «ОЗУ ср 0,4 МиБ при максимуме 17,5» и «CPU ср 16% при максимуме
#     5%». Среднее больше максимума — признак того, что замер врёт, а не того,
#     что нагрузка неровная.
#  4. Первый снимок каждого прогона отбрасывается: он снят до того, как нагрузка
#     разогналась (в проверке давал 0,11% CPU против 26-64% на остальных).
#  5. Нагрузка длинная (по умолчанию 20 000 запросов, около минуты): снимок
#     стоит ~1,3 с, и на коротком прогоне точек просто не набирается.
#  6. Каждый режим прогоняется дважды, в отчёт идут оба числа: разброс важнее
#     красивого среднего.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

REPORT_ONLY=0
if [ "${1:-}" = "--report-only" ]; then
  REPORT_ONLY=1
  shift
fi

REQUESTS="${1:-20000}"
REPEATS="${2:-2}"
WARMUP=600
CONCURRENCY=8
RESULT_FILE="$STAND_DIR/results/02-instrumentation.txt"
TMP="$STAND_DIR/results/.bench"

if [ "$REPORT_ONLY" = "0" ]; then
  rm -rf "$TMP"
fi
mkdir -p "$TMP"

SAMPLER_PID=""

# Сбор снимков в фоне, пока не удалён файл-флаг.
sample_start() {
  local out="$1"
  : > "$out"
  touch "$TMP/.sampling"
  (
    while [ -f "$TMP/.sampling" ]; do
      docker stats --no-stream --format "{{.Name}};{{.CPUPerc}};{{.MemUsage}}" \
        ops-go-frontend ops-java-backend >> "$out" 2>/dev/null || true
    done
  ) &
  SAMPLER_PID=$!
}

sample_stop() {
  rm -f "$TMP/.sampling"
  if [ -n "$SAMPLER_PID" ]; then
    wait "$SAMPLER_PID" 2>/dev/null || true
    SAMPLER_PID=""
  fi
}

summarize_stats() {
  local file="$1"
  awk -F';' '
  # Прочерки означают, что docker не отдал статистику по контейнеру. Их доля
  # печатается рядом: замер, из которого выброшено много точек, должен вызывать
  # подозрение, а не молча усредняться.
  { name=$1; total[name]++
    if ($2 ~ /^--/ || $3 ~ /^--/) { skipped[name]++; next }
    seen[name]++
    if (seen[name] == 1) { first[name]++; next }   # первый снимок — до разгона
    cpu=$2; gsub("%","",cpu); split($3,m," / "); v=m[1]; u=v
    gsub(/[A-Za-z]+/,"",v); gsub(/[0-9.]+/,"",u)
    if (u=="GiB") v=v*1024
    # +0 обязательно: без приведения к числу awk сравнивает как СТРОКИ, и
    # "104.32" оказывается меньше "8.7". Из-за этого в отчёте появлялся
    # максимум CPU 8,7% при среднем 47,9% — среднее больше максимума.
    cpu = cpu + 0; v = v + 0
    n[name]++; c[name]+=cpu; s[name]+=v
    if (v>mx[name]+0) mx[name]=v
    if (cpu>cmx[name]+0) cmx[name]=cpu }
  END { for (k in total) {
          if (n[k] == 0) { printf "    %-18s нет годных точек из %d\n", k, total[k]; continue }
          printf "    %-18s CPU ср %6.1f%%  CPU макс %6.1f%%  ОЗУ ср %6.1f МиБ  ОЗУ макс %6.1f МиБ  (%d точек, прочерков %d)\n",
            k, c[k]/n[k], cmx[k], s[k]/n[k], mx[k], n[k], skipped[k]+0 } }' "$file" | sort
}

measure() {
  local mode="$1" label="$2" pass="$3"
  local tag="${label}-${pass}"

  echo "  подъём в режиме $mode (проход $pass)" >&2
  ./scripts/down.sh clean >/dev/null 2>&1
  ./scripts/up.sh all "$mode" >/dev/null 2>&1

  echo "  прогрев $WARMUP запросами" >&2
# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
  docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
    -requests "$WARMUP" -seed 1 -concurrency "$CONCURRENCY" >/dev/null 2>&1 || true
  sleep 5

  echo "  замер $REQUESTS запросов" >&2
  sample_start "$TMP/stats-$tag.txt"

  docker compose run --rm --no-deps -T loadgen -target http://go-frontend:8080 \
    -requests "$REQUESTS" -seed 42 -concurrency "$CONCURRENCY" \
    -run "bench-$tag" -json 2>/dev/null | sed -n '/^{/,$p' > "$TMP/load-$tag.json" || true

  sample_stop

  "$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
l = d['latency']
print('%s;%.1f;%.1f;%.1f;%.1f;%s;%s' % ('$tag', l['p50_ms'], l['p90_ms'], l['p99_ms'],
      l['max_ms'], d['duration'], d['mismatch']))
" < "$TMP/load-$tag.json"
}

if [ "$REPORT_ONLY" = "1" ]; then
  echo "=== Только отчёт, из сохранённых данных в results/.bench/"
else
  echo "=== Накладные расходы: $REQUESTS запросов, $REPEATS повтора на режим"
  : > "$TMP/latency.csv"
  for pass in $(seq 1 "$REPEATS"); do
    for mode_pair in "off:baseline" "otel:grpc" "http:httpproto"; do
      mode="${mode_pair%%:*}"
      label="${mode_pair##*:}"
      echo "-- режим $mode, проход $pass"
      measure "$mode" "$label" "$pass" >> "$TMP/latency.csv"
    done
  done
fi

{
  echo "Накладные расходы инструментирования, $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo "======================================================================"
  echo "Нагрузка: $REQUESTS запросов, параллельно $CONCURRENCY, $REPEATS повтора на режим."
  echo "Перед каждым замером стенд поднят заново с чистыми томами, прогрет"
  echo "$WARMUP запросами, затем пауза 5 с: у JVM без прогрева измеряется разогрев."
  echo "Ресурсы — цикл снимков docker stats --no-stream во время нагрузки; первый"
  echo "снимок отброшен (снят до разгона). Потоковый docker stats на этой машине"
  echo "под нагрузкой отдавал прочерки в 97% строк и делал средние бессмысленными."
  echo "Латентность считается по успешным запросам БЕЗ медленного SKU-0004:"
  echo "pg_sleep(0.15) в нём иначе перетягивает перцентили и скрывает разницу."
  echo
  echo "Режимы:"
  echo "  baseline  — Go: OTEL_SDK_DISABLED=true, Java: без -javaagent"
  echo "  grpc      — Go SDK по OTLP/gRPC, Java-агент (его дефолт http/protobuf)"
  echo "  httpproto — Go SDK по OTLP/HTTP, Java-агент"
  echo
  echo "ЛАТЕНТНОСТЬ, мс"
  printf "%-14s %8s %8s %8s %8s %10s %10s\n" "ПРОГОН" "p50" "p90" "p99" "max" "время" "расхожд."
  while IFS=';' read -r tag p50 p90 p99 mx dur mism; do
    printf "%-14s %8s %8s %8s %8s %10s %10s\n" "$tag" "$p50" "$p90" "$p99" "$mx" "$dur" "$mism"
  done < "$TMP/latency.csv"
  echo
  echo "РЕСУРСЫ ПРИЛОЖЕНИЙ (CPU в процентах ОДНОГО ядра: 100% и выше значит"
  echo "нагрузку больше одного ядра)"
  for f in "$TMP"/stats-*.txt; do
    [ -f "$f" ] || continue
    name="$(basename "$f" .txt | sed 's/^stats-//')"
    echo "  $name"
    summarize_stats "$f"
  done
} > "$RESULT_FILE"

cat "$RESULT_FILE"
echo
echo "записано: results/02-instrumentation.txt"
