#!/usr/bin/env bash
# Что делает с каждым бэкендом поле с уникальным значением на запись.
#
#   ./scripts/bench-cardinality.sh [записей] [seed] [сценарий]
#
# Сценарий: plain | body | label | report | all (по умолчанию all).
# Прогоны разделены намеренно: каждый занимает минуты, а прогон label при
# взрыве кардинальности — до получаса. Запуск по одному укладывается в разумное
# время и переживает обрыв сессии; `report` собирает отчёт из сохранённых
# результатов.
#
# Два прогона на ЧИСТЫХ томах: без высококардинального поля и с ним. Разница
# между прогонами и есть результат.
#
# КРИТЕРИЙ СВОЙ ДЛЯ КАЖДОГО, и это не небрежность. Механизмы разные:
#   Loki:         поле уходит в МЕТКУ, и каждое значение порождает новый поток;
#   OpenSearch:   поле попадает в МАППИНГ, растёт число полей индекса;
#   VictoriaLogs: поле остаётся полем, поток задаётся отдельно (_stream_fields).
# Подгонять три разных механизма под одну метрику — значит сравнивать
# несравнимое; вместо этого меряется то, что у каждого растёт, и объём как
# общий знаменатель.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

COUNT="${1:-100000}"
SEED="${2:-42}"
CASE="${3:-all}"
RESULT_FILE="$STAND_DIR/results/04-cardinality.txt"

VOL_LOKI="log-backends_loki-data"
VOL_OS="log-backends_os-data"
VOL_VL="log-backends_vlogs-data"
DATA_LOKI="/data/chunks /data/tsdb-shipper-active"
DATA_OS="/data/nodes"
DATA_VL="/data/partitions"

SETTLE_SECONDS="${SETTLE_SECONDS:-150}"

# --- показатели каждого бэкенда ----------------------------------------------

# Loki: число СЕРИЙ (уникальных комбинаций меток) по API.
#
# Не метрика loki_ingester_memory_streams: во-первых, она приходит с лейблом
# tenant и парсер по префиксу со пробелом её не видит; во-вторых, она считает
# потоки В ПАМЯТИ ingester, а те уходят оттуда после сброса чанков — при
# коротком chunk_idle_period значение почти всегда ноль. API /series отвечает
# на нужный вопрос: сколько различных комбинаций меток лежит в хранилище.
loki_streams() {
  innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/series"     --data-urlencode 'match[]={service=~".+"}'     --data-urlencode "start=$(( $(date +%s) - 7200 ))000000000"     --data-urlencode "end=$(date +%s)000000000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(len(json.load(sys.stdin).get('data', [])))
except Exception:
    print(0)
"
}

# OpenSearch: число полей в маппинге индекса. Динамический маппинг создаёт
# запись на каждый новый ключ, и это его способ подавиться кардинальностью —
# не потоками, а шириной схемы.
os_fields() {
  innet -s --max-time 20 "http://opensearch:9200/logs-default/_mapping" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json

def count(node):
    total = 0
    for name, body in (node or {}).items():
        total += 1
        if isinstance(body, dict):
            total += count(body.get('properties'))
            total += len(body.get('fields') or {})
    return total

try:
    d = json.load(sys.stdin)
    idx = next(iter(d.values()))
    print(count(idx['mappings'].get('properties')))
except Exception:
    print(0)
"
}

# VictoriaLogs: число ПОТОКОВ. У него поток задаётся явно через _stream_fields,
# поэтому высококардинальное поле само по себе потоков не плодит — но проверить
# это надо, а не предположить.
vl_streams() {
  innet -s --max-time 20 -X POST "http://victorialogs:9428/select/logsql/query" \
    -d 'query=* | count_uniq(_stream_id)' 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    line = sys.stdin.readline().strip()
    d = json.loads(line) if line else {}
    print(int(next(iter(d.values()))) if d else 0)
except Exception:
    print(0)
"
}

count_all() {
  local n_loki n_os n_vl
  n_loki="$(innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range" \
    --data-urlencode 'query=sum(count_over_time({service=~".+"}[2h]))' \
    --data-urlencode "start=$(( $(date +%s) - 7200 ))000000000" \
    --data-urlencode "end=$(date +%s)000000000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    r = json.load(sys.stdin)['data']['result']
    print(int(float(r[0]['values'][-1][1])) if r else 0)
except Exception:
    print(0)
")"
  n_os="$(innet -s --max-time 30 "http://opensearch:9200/logs-default/_count" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(int(json.load(sys.stdin).get('count', 0)))
except Exception:
    print(0)
")"
  n_vl="$(innet -s --max-time 30 -X POST "http://victorialogs:9428/select/logsql/query" \
    -d 'query=* | count()' 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    line = sys.stdin.readline().strip()
    print(int(json.loads(line)['count(*)']) if line else 0)
except Exception:
    print(0)
")"
  echo "$n_loki $n_os $n_vl"
}

# --- один прогон -------------------------------------------------------------

run_case() {
  local label="$1" highcard="$2" mode="$3"

  log "ПРОГОН $label: тома чистые, поле: $highcard, режим стенда: $mode"
  ./scripts/down.sh clean >/dev/null 2>&1 || true
  ./scripts/up.sh "$mode" >/dev/null 2>&1

  local args=(-count "$COUNT" -seed "$SEED" -run "card-$label")
  [ "$highcard" = "да" ] && args+=(-high-cardinality)
  docker compose --profile tools run --rm -T loadgen "${args[@]}" >/dev/null 2>&1

  # КОНТРОЛЬ: опыт обязан доказать, что он состоялся. Без этого «поле не
  # повлияло» неотличимо от «поля не было».
  local has_field
  has_field="$(docker run --rm -v log-backends_logs:/logs alpine:3.21 \
    sh -c 'head -1 /logs/app.log | grep -c request_id || true' 2>/dev/null)"
  echo "  request_id в записях: $has_field (ожидается $([ "$highcard" = "да" ] && echo 1 || echo 0))"
  if { [ "$highcard" = "да" ] && [ "$has_field" != "1" ]; } ||
     { [ "$highcard" = "нет" ] && [ "$has_field" != "0" ]; }; then
    echo "ОШИБКА: режим не применился — прогон $label недостоверен." >&2
    exit 1
  fi

  # Ожидание доставки с ЗАМЕРОМ ВРЕМЕНИ и контролем полноты.
  #
  # Полнота здесь не формальность: в прогоне label первая редакция скрипта
  # ждала 300 с, не дождалась (Loki 16364, остальные 23298 из 100000) и всё
  # равно посчитала объёмы — то есть выдала числа по четверти данных за
  # результат. Проверки полноты в этом скрипте просто не было, в отличие от
  # bench-storage.
  #
  # А само замедление — результат, а не помеха: при взрыве кардинальности
  # приём деградирует, и время доставки стоит измерить.
  local n_loki n_os n_vl waited=0
  local limit="${DELIVERY_LIMIT:-1200}"
  local started
  started="$(date +%s)"
  while [ "$waited" -lt "$limit" ]; do
    read -r n_loki n_os n_vl <<<"$(count_all)"
    if [ "$n_loki" -ge "$COUNT" ] && [ "$n_os" -ge "$COUNT" ] && [ "$n_vl" -ge "$COUNT" ] 2>/dev/null; then
      break
    fi
    sleep 10
    waited=$(( $(date +%s) - started ))
  done
  waited=$(( $(date +%s) - started ))
  echo "  доехало за ${waited}с: Loki $n_loki, OpenSearch $n_os, VictoriaLogs $n_vl"

  # Недоставка в режиме label — НЕ ошибка прогона, а его результат: Loki
  # упирается в лимит активных потоков и начинает отвечать 429. Полная доставка
  # в этом режиме невозможна на значениях по умолчанию, и это главное, что тут
  # надо показать.
  #
  # А вот для plain и body недоставка означает поломку стенда: там поток
  # проходит за двадцать секунд.
  local complete=yes
  if [ "$n_loki" -lt "$COUNT" ] || [ "$n_os" -lt "$COUNT" ] || [ "$n_vl" -lt "$COUNT" ] 2>/dev/null; then
    complete=no
    if [ "$label" != "label" ]; then
      echo "ОШИБКА: за ${limit}с доехало не всё, а в режиме $label поток проходит" >&2
      echo "        за секунды — значит сломан стенд, а не бэкенд." >&2
      exit 1
    fi
    echo "  ДОСТАВКА НЕ ЗАВЕРШИЛАСЬ — это и есть результат режима label."
  fi

  # Отказы приёма: сколько раз Loki ответил 429 и на чём именно.
  local rejects
  rejects="$(docker logs lb-loki 2>&1 | grep -c 'maximum active stream limit exceeded' || true)"
  echo "  отказов Loki «maximum active stream limit exceeded»: $rejects"

  # Принудительное слияние у OpenSearch — обязательно, иначе его объём зависит
  # от того, успел ли пройти фоновый merge. Без этого шага прогон label показал
  # 8.7 МиБ против 20.7 МиБ у plain, то есть МЕНЬШЕ при БОЛЬШЕМ объёме данных:
  # замер поймал незаконченное слияние и выдал бессмыслицу.
  innet -s --max-time 90 -X POST "http://opensearch:9200/logs-default/_flush" >/dev/null 2>&1 || true
  # wait_for_completion=true и длинный таймаут: без них запрос возвращается
  # сразу, слияние идёт в фоне, и замер ловит СЛУЧАЙНУЮ стадию. Именно так
  # объём OpenSearch скакал между прогонами: 20.7, 6.0, 4.3, 2.9 МиБ на одних и
  # тех же данных.
  innet -s --max-time 300 -X POST     "http://opensearch:9200/logs-default/_forcemerge?max_num_segments=1&wait_for_completion=true"     >/dev/null 2>&1 || true
  # Контрольная точка: слияние действительно завершилось, сегмент один.
  local segs
  segs="$(innet -s --max-time 30 "http://opensearch:9200/_cat/segments/logs-default?h=segment" 2>/dev/null | wc -l | tr -d ' ')"
  echo "  сегментов в индексе OpenSearch после слияния: $segs"

  sleep "$SETTLE_SECONDS"

  local streams fields vstreams size_loki size_os size_vl
  streams="$(loki_streams)"
  fields="$(os_fields)"
  vstreams="$(vl_streams)"
  size_loki="$(data_bytes "$VOL_LOKI" $DATA_LOKI)"
  size_os="$(data_bytes "$VOL_OS" $DATA_OS)"
  size_vl="$(data_bytes "$VOL_VL" $DATA_VL)"

  echo "  Loki:         потоков $streams, данные $(human "$size_loki")"
  echo "  OpenSearch:   полей в маппинге $fields, данные $(human "$size_os")"
  echo "  VictoriaLogs: потоков $vstreams, данные $(human "$size_vl")"

  # Одиннадцать полей: к размерам и числу потоков добавлены время доставки,
  # признак её завершённости, принятое каждым бэкендом и число отказов Loki.
  # Без них таблица «доставка» в отчёте пуста, а именно она и содержит главный
  # результат режима label.
  printf '%s %s %s %s %s %s %s %s %s %s %s\n' "$streams" "$fields" "$vstreams" \
    "$size_loki" "$size_os" "$size_vl" "$waited" "$complete" \
    "$n_loki" "$n_os" "$rejects" > "$STAND_DIR/results/.card-$label.txt"
}

# --- два прогона -------------------------------------------------------------

# Три сценария, а не два. Разница между вторым и третьим — суть замера:
#   plain — уникального поля нет вовсе;
#   body  — поле есть, но лежит в ТЕЛЕ записи (правильный способ);
#   label — то же поле объявлено МЕТКОЙ Loki и частью потока VictoriaLogs
#           (типичная ошибка).
case "$CASE" in
  plain)  run_case plain нет default;  exit 0 ;;
  body)   run_case body  да  default;  exit 0 ;;
  label)  run_case label да  highcard; exit 0 ;;
  label-resume)
    # Продолжить ожидание доставки в уже поднятом стенде и померить.
    run_case label да highcard resume; exit 0 ;;
  report) : ;;   # только собрать отчёт из готовых результатов
  all)
    run_case plain нет default
    run_case body  да  default
    run_case label да  highcard
    ;;
  *)
    echo "неизвестный сценарий: $CASE (ожидается plain, body, label, label-resume, report или all)" >&2
    exit 2
    ;;
esac

for f in plain body label; do
  if [ ! -f "$STAND_DIR/results/.card-$f.txt" ]; then
    echo "ОШИБКА: нет результата сценария $f. Прогоните его:" >&2
    echo "        ./scripts/bench-cardinality.sh $COUNT $SEED $f" >&2
    exit 1
  fi
done

read -r S_PLAIN F_PLAIN V_PLAIN SL_PLAIN SO_PLAIN SV_PLAIN T_PLAIN C_PLAIN NL_PLAIN NO_PLAIN R_PLAIN < "$STAND_DIR/results/.card-plain.txt"
read -r S_BODY  F_BODY  V_BODY  SL_BODY  SO_BODY  SV_BODY  T_BODY  C_BODY  NL_BODY  NO_BODY  R_BODY  < "$STAND_DIR/results/.card-body.txt"
read -r S_LABEL F_LABEL V_LABEL SL_LABEL SO_LABEL SV_LABEL T_LABEL C_LABEL NL_LABEL NO_LABEL R_LABEL < "$STAND_DIR/results/.card-label.txt"

# --- отчёт -------------------------------------------------------------------

read -r G_S_BODY G_S_LABEL G_F_BODY G_V_LABEL G_L_BODY G_L_LABEL G_O_BODY G_V2_BODY G_V2_LABEL <<<"$("$PYTHON_BIN" -c "
def r(a, b):
    return '%.1f' % (b / a) if a else 'n/a'
print(r($S_PLAIN, $S_BODY), r($S_PLAIN, $S_LABEL),
      r($F_PLAIN, $F_BODY), r($V_PLAIN, $V_LABEL),
      r($SL_PLAIN, $SL_BODY), r($SL_PLAIN, $SL_LABEL),
      r($SO_PLAIN, $SO_BODY),
      r($SV_PLAIN, $SV_BODY), r($SV_PLAIN, $SV_LABEL))
")"

mkdir -p "$STAND_DIR/results"
{
  echo "Высококардинальное поле в трёх моделях индексации"
  echo "=================================================="
  echo "Три прогона по $COUNT записей, seed $SEED, чистые тома."
  echo
  echo "  plain — уникального поля нет вовсе"
  echo "  body  — request_id есть, лежит в ТЕЛЕ записи (правильный способ)"
  echo "  label — тот же request_id объявлен МЕТКОЙ Loki и частью потока"
  echo "          VictoriaLogs (типичная ошибка). У OpenSearch аналога нет:"
  echo "          понятия потока в его модели не существует."
  echo
  echo "ЧИСЛО ПОТОКОВ И ПОЛЕЙ"
  printf "%-16s %-24s %8s %8s %8s
" "БЭКЕНД" "ЧТО СЧИТАЕМ" "PLAIN" "BODY" "LABEL"
  printf "%-16s %-24s %8s %8s %8s
" "Loki"         "серий (комбинаций меток)" "$S_PLAIN" "$S_BODY" "$S_LABEL"
  printf "%-16s %-24s %8s %8s %8s
" "OpenSearch"   "полей в маппинге"         "$F_PLAIN" "$F_BODY" "$F_LABEL"
  printf "%-16s %-24s %8s %8s %8s
" "VictoriaLogs" "потоков (_stream_id)"     "$V_PLAIN" "$V_BODY" "$V_LABEL"
  echo
  echo "ОБЪЁМ ДАННЫХ"
  printf "%-16s %12s %12s %12s
" "БЭКЕНД" "PLAIN" "BODY" "LABEL"
  printf "%-16s %12s %12s %12s
" "Loki"         "$(human "$SL_PLAIN")" "$(human "$SL_BODY")" "$(human "$SL_LABEL")"
  printf "%-16s %12s %12s %12s
" "OpenSearch"   "$(human "$SO_PLAIN")" "$(human "$SO_BODY")" "$(human "$SO_LABEL")"
  printf "%-16s %12s %12s %12s
" "VictoriaLogs" "$(human "$SV_PLAIN")" "$(human "$SV_BODY")" "$(human "$SV_LABEL")"
  echo
  echo "РОСТ ОТНОСИТЕЛЬНО PLAIN"
  echo "  Loki, серий:            body ${G_S_BODY}x, label ${G_S_LABEL}x"
  echo "  Loki, объём:            body ${G_L_BODY}x, label ${G_L_LABEL}x"
  echo "  OpenSearch, полей:      body ${G_F_BODY}x"
  echo "  OpenSearch, объём:      body ${G_O_BODY}x"
  echo "  VictoriaLogs, потоков:  label ${G_V_LABEL}x"
  echo "  VictoriaLogs, объём:    body ${G_V2_BODY}x, label ${G_V2_LABEL}x"
  echo
  echo "ДОСТАВКА"
  printf "%-8s %-12s %10s %12s %10s
" "РЕЖИМ" "ЗАВЕРШЕНА" "ВРЕМЯ,с" "LOKI ПРИНЯЛ" "ОТКАЗОВ"
  printf "%-8s %-12s %10s %12s %10s
" "plain" "$C_PLAIN" "$T_PLAIN" "$NL_PLAIN" "$R_PLAIN"
  printf "%-8s %-12s %10s %12s %10s
" "body"  "$C_BODY"  "$T_BODY"  "$NL_BODY"  "$R_BODY"
  printf "%-8s %-12s %10s %12s %10s
" "label" "$C_LABEL" "$T_LABEL" "$NL_LABEL" "$R_LABEL"
  echo
  echo "ГЛАВНЫЙ РЕЗУЛЬТАТ: В РЕЖИМЕ LABEL ДОСТАВКА НЕ ЗАВЕРШАЕТСЯ ВОВСЕ"
  echo "Loki упирается в лимит активных потоков и отвечает 429:"
  echo
  echo "  maximum active stream limit exceeded when trying to create stream"
  echo "  {level=\"info\", request_id=\"req-...\", service=\"checkout\", ...}"
  echo "  reduce the number of active streams (reduce labels or reduce label values)"
  echo
  echo "То есть дело не в том, что «стало медленнее»: система ОТКАЗЫВАЕТСЯ"
  echo "принимать поток, и Vector уходит в бесконечные повторы. Цифры объёма и"
  echo "числа потоков в строке label относятся к ЧАСТИ данных ($NL_LABEL из"
  echo "$COUNT у Loki) — сравнивать их с plain и body как равные нельзя."
  echo
  echo "ВТОРОЙ РЕЗУЛЬТАТ: ОТКАЗ ОДНОГО СИНКА ТОРМОЗИТ ВЕСЬ КОНВЕЙЕР"
  echo "OpenSearch и VictoriaLogs в этом режиме приняли $NO_LABEL записей —"
  echo "столько же, сколько успел отдать общий Vector. Они сами ограничения не"
  echo "достигли: их тормозит соседний синк, потому что источник один и общий."
  echo "Раздельные буферы тут ни при чём — в Vector буфер у каждого синка и так"
  echo "свой. Останавливает конвейер ПОЛИТИКА ПЕРЕПОЛНЕНИЯ: по умолчанию"
  echo "when_full: block, и заполненный буфер отказавшего синка тормозит отдачу"
  echo "вверх по топологии. Что даёт другая политика — results/06-isolation.txt."
  echo
  echo "ГЛАВНОЕ: БЕДА НЕ В ПОЛЕ, А В ТОМ, КУДА ЕГО ПОЛОЖИЛИ"
  echo "Уникальное значение в ТЕЛЕ записи стоит объёма — и только. Оно же,"
  echo "объявленное меткой или частью потока, множит сущности, по которым"
  echo "строится индекс. Это две разные беды с разной ценой, и управляет ими"
  echo "НАСТРОЙКА КОНВЕЙЕРА, а не сам факт наличия идентификатора в логе:"
  echo "  Loki:         какие поля перечислены в labels у синка;"
  echo "  VictoriaLogs: что указано в _stream_fields при приёме;"
  echo "  OpenSearch:   динамический маппинг — новый КЛЮЧ становится полем"
  echo "                (уникальные ЗНАЧЕНИЯ схему не расширяют, они бьют"
  echo "                по объёму индекса)."
  echo
  echo "ЧТО НЕ ПРОВЕРЯЛОСЬ"
  echo "Поведение на пределе: лимиты (max_streams_per_user у Loki,"
  echo "index.mapping.total_fields.limit у OpenSearch) здесь не достигались."
  echo "Стенд показывает МЕХАНИЗМ роста, а не момент отказа."
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"
