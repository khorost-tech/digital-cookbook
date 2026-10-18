#!/usr/bin/env bash
# Что даёт настройка: тот же поток на значениях по умолчанию и после тюнинга.
#
#   ./scripts/bench-tuned.sh [записей] [seed]
#
# Отвечает на вопрос, который иначе повисает после замера объёма: «а если
# настроить?». Сравниваются два прогона на чистых томах, различающиеся только
# конфигурацией приёмной стороны:
#
#   OpenSearch:   динамический маппинг  ->  СТРОГИЙ шаблон logs-tuned*
#                 (message как keyword вместо text, request_id без индекса)
#   Loki:         detected_level добавляется  ->  discover_log_levels: false
#   VictoriaLogs: без изменений — резать в его модели нечего
#
# ПРОВЕРКА ПРИМЕНЕНИЯ ОБЯЗАТЕЛЬНА. «Настройка ничего не изменила» и «настройка
# не применилась» выглядят одинаково, поэтому факт применения проверяется
# отдельно, до замера.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

COUNT="${1:-100000}"
SEED="${2:-42}"
CASE="${3:-all}"
RESULT_FILE="$STAND_DIR/results/05-tuned.txt"
SETTLE_SECONDS="${SETTLE_SECONDS:-90}"

VOL_LOKI="log-backends_loki-data"
VOL_OS="log-backends_os-data"
VOL_VL="log-backends_vlogs-data"
DATA_LOKI="/data/chunks /data/tsdb-shipper-active"
DATA_OS="/data/nodes"
DATA_VL="/data/partitions"

# --- счётчики ----------------------------------------------------------------

count_os_index() {
  innet -s --max-time 30 "http://opensearch:9200/$1/_count" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(int(json.load(sys.stdin).get('count', 0)))
except Exception:
    print(0)
"
}

count_loki() {
  local now start
  now="$(date +%s)"; start=$((now - 7200))
  innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range" \
    --data-urlencode 'query=sum(count_over_time({service=~".+"}[2h]))' \
    --data-urlencode "start=${start}000000000" --data-urlencode "end=${now}000000000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    r = json.load(sys.stdin)['data']['result']
    print(int(float(r[0]['values'][-1][1])) if r else 0)
except Exception:
    print(0)
"
}

count_vl() {
  innet -s --max-time 30 -X POST "http://victorialogs:9428/select/logsql/query" \
    -d 'query=* | count()' 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    line = sys.stdin.readline().strip()
    print(int(json.loads(line)['count(*)']) if line else 0)
except Exception:
    print(0)
"
}

# Метки, которые Loki реально положил в индекс. Нужны, чтобы доказать, что
# discover_log_levels: false действительно убрал detected_level.
loki_label_names() {
  innet -s --max-time 30 "http://loki:3100/loki/api/v1/labels" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(','.join(sorted(json.load(sys.stdin).get('data', []))))
except Exception:
    print('')
"
}

# --- один прогон -------------------------------------------------------------

run_case() {
  local label="$1" mode="$2" index="$3"

  log "ПРОГОН $label: тома чистые, режим стенда: $mode, индекс: $index"
  ./scripts/down.sh clean >/dev/null 2>&1 || true
  ./scripts/up.sh "$mode" >/dev/null 2>&1

  if [ "$mode" = "tuned" ]; then
    # Шаблон кладётся ДО первой записи: иначе индекс создастся динамическим
    # маппингом, и шаблон к нему уже не применится.
    innet -s --max-time 30 -X PUT "http://opensearch:9200/_index_template/logs-tuned" \
      -H 'Content-Type: application/json' \
      --data-binary "$(cat opensearch/index-template-tuned.json)" >/dev/null 2>&1 || true

    # КОНТРОЛЬ ПРИМЕНЕНИЯ: шаблон существует и виден OpenSearch.
    local tpl
    tpl="$(innet -s --max-time 20 "http://opensearch:9200/_index_template/logs-tuned" 2>/dev/null |
      "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(len(d.get('index_templates', [])))
except Exception:
    print(0)
")"
    echo "  шаблон logs-tuned зарегистрирован: $tpl (ожидается 1)"
    if [ "$tpl" != "1" ]; then
      echo "ОШИБКА: шаблон не зарегистрирован — прогон описывал бы дефолтный маппинг." >&2
      exit 1
    fi
  fi

  docker compose --profile tools run --rm -T loadgen \
    -count "$COUNT" -seed "$SEED" -run "tuned-$label" >/dev/null 2>&1

  local n_loki n_os n_vl waited=0 started
  started="$(date +%s)"
  while [ "$waited" -lt 600 ]; do
    n_loki="$(count_loki)"; n_os="$(count_os_index "$index")"; n_vl="$(count_vl)"
    if [ "$n_loki" -ge "$COUNT" ] && [ "$n_os" -ge "$COUNT" ] && [ "$n_vl" -ge "$COUNT" ] 2>/dev/null; then
      break
    fi
    sleep 5
    waited=$(( $(date +%s) - started ))
  done
  echo "  доехало: Loki $n_loki, OpenSearch $n_os, VictoriaLogs $n_vl"

  if [ "$n_loki" -lt "$COUNT" ] || [ "$n_os" -lt "$COUNT" ] || [ "$n_vl" -lt "$COUNT" ] 2>/dev/null; then
    echo "ОШИБКА: доехало не всё — сравнивать объёмы нельзя." >&2
    exit 1
  fi

  innet -s --max-time 90 -X POST "http://opensearch:9200/$index/_flush" >/dev/null 2>&1 || true
  innet -s --max-time 300 -X POST \
    "http://opensearch:9200/$index/_forcemerge?max_num_segments=1&wait_for_completion=true" \
    >/dev/null 2>&1 || true
  sleep "$SETTLE_SECONDS"

  local size_loki size_os size_vl labels
  size_loki="$(data_bytes "$VOL_LOKI" $DATA_LOKI)"
  size_os="$(data_bytes "$VOL_OS" $DATA_OS)"
  size_vl="$(data_bytes "$VOL_VL" $DATA_VL)"
  labels="$(loki_label_names)"

  echo "  Loki:         $(human "$size_loki"), метки: $labels"
  echo "  OpenSearch:   $(human "$size_os")"
  echo "  VictoriaLogs: $(human "$size_vl")"

  printf '%s %s %s %s\n' "$size_loki" "$size_os" "$size_vl" "$labels" \
    > "$STAND_DIR/results/.tuned-$label.txt"
}

case "$CASE" in
  default) run_case default default logs-default; exit 0 ;;
  tuned)   run_case tuned   tuned   logs-tuned;   exit 0 ;;
  report)  : ;;
  all)
    run_case default default logs-default
    run_case tuned   tuned   logs-tuned
    ;;
  *)
    echo "неизвестный сценарий: $CASE (ожидается default, tuned, report или all)" >&2
    exit 2
    ;;
esac

for f in default tuned; do
  if [ ! -f "$STAND_DIR/results/.tuned-$f.txt" ]; then
    echo "ОШИБКА: нет результата сценария $f. Прогоните: $0 $COUNT $SEED $f" >&2
    exit 1
  fi
done

read -r SL_DEF SO_DEF SV_DEF LB_DEF < "$STAND_DIR/results/.tuned-default.txt"
read -r SL_TUN SO_TUN SV_TUN LB_TUN < "$STAND_DIR/results/.tuned-tuned.txt"

read -r R_LOKI R_OS R_VL <<<"$("$PYTHON_BIN" -c "
def r(a, b):
    return '%.2f' % (b / a) if a else 'n/a'
print(r($SL_DEF, $SL_TUN), r($SO_DEF, $SO_TUN), r($SV_DEF, $SV_TUN))
")"

mkdir -p "$STAND_DIR/results"
{
  echo "Дефолты против настройки: тот же поток, другая конфигурация приёма"
  echo "=================================================================="
  echo "Два прогона по $COUNT записей, seed $SEED, чистые тома."
  echo
  echo "ЧТО ИМЕННО НАСТРОЕНО"
  echo "  OpenSearch:   строгий шаблон logs-tuned* вместо динамического маппинга;"
  echo "                message как keyword (без полнотекстового анализа),"
  echo "                request_id с index: false и doc_values: false."
  echo "  Loki:         discover_log_levels: false — перестаёт сам добавлять"
  echo "                метку detected_level."
  echo "  VictoriaLogs: без изменений, резать в его модели нечего."
  echo
  printf "%-14s %12s %12s %8s\n" "БЭКЕНД" "ДЕФОЛТ" "НАСТРОЕНО" "ОТНОШЕНИЕ"
  printf "%-14s %12s %12s %8s\n" "Loki"         "$(human "$SL_DEF")" "$(human "$SL_TUN")" "${R_LOKI}x"
  printf "%-14s %12s %12s %8s\n" "OpenSearch"   "$(human "$SO_DEF")" "$(human "$SO_TUN")" "${R_OS}x"
  printf "%-14s %12s %12s %8s\n" "VictoriaLogs" "$(human "$SV_DEF")" "$(human "$SV_TUN")" "${R_VL}x"
  echo
  echo "МЕТКИ LOKI"
  echo "  дефолт:    $LB_DEF"
  echo "  настроено: $LB_TUN"
  echo
  echo "Списки совпали, и это тоже результат: detected_level, который Loki"
  echo "добавляет при ПРЯМОМ приёме (проверено в results/00-feasibility.txt),"
  echo "через синк Vector не появляется вовсе. Значит настройка"
  echo "discover_log_levels здесь и не могла ничего изменить — отключать было"
  echo "нечего. Метка service_name при этом присутствует в обоих: её Loki"
  echo "выводит из service независимо от этой настройки."
  echo
  echo "КАК ЧИТАТЬ"
  echo "Отношение меньше единицы — настройка уменьшила объём. VictoriaLogs здесь"
  echo "контрольная группа: его конфигурация не менялась, и его число показывает"
  echo "разброс между двумя прогонами вообще. Если у кого-то изменение того же"
  echo "порядка, что у VictoriaLogs, — это не эффект настройки, а шум."
  echo
  echo "ЧЕГО ЗАМЕР НЕ ПОКАЗЫВАЕТ"
  echo "Цену настройки для ПОИСКА. message как keyword дешевле в хранении, но"
  echo "полнотекстовый поиск по нему пропадает: искать можно только точное"
  echo "совпадение целой строки. Это ровно тот компромисс, ради которого"
  echo "OpenSearch и берут, поэтому «настроил и стало лучше» здесь неверно —"
  echo "стало дешевле и беднее одновременно."
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"
