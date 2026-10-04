#!/usr/bin/env bash
# Самопроверка exemplars: ведёт ли прыжок из точки метрики в НУЖНЫЙ трейс.
#
#   ./scripts/verify-exemplars.sh [запросов]
#
# Требует режима: ./scripts/up.sh all spanmetrics
#
# Цепочка metric -> trace рвётся в трёх разных местах, и каждый разрыв выглядит
# одинаково: дежурный кликает по точке на графике и попадает в пустой экран.
# Поэтому здесь три отдельных критерия, а не один.
#
#   1) exemplar вообще дошёл до Prometheus и хранится;
#   2) trace_id из exemplar находится в Tempo;
#   3) найденный трейс — ТОТ САМЫЙ: тот сервис и та операция, по которым
#      посчитана метрика.
#
# Без третьего критерия проверка подтвердила бы любой существующий трейс: в
# стенде их тысячи, и «трейс нашёлся» ничего не значит.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"
require_tools

REQUESTS="${1:-200}"
SEED="${2:-42}"
RESULT_FILE="$STAND_DIR/results/12-exemplars.txt"

# Сервис и операция, по которым берётся exemplar и которые обязаны оказаться в
# найденном трейсе.
EX_SERVICE="go-frontend"
EX_SPAN="POST /order"

# Метрика, по которой ищем exemplars. Именно гистограмма длительности: exemplars
# коннектор прикрепляет к бакетам гистограммы, а не к счётчику вызовов.
#
# СЕРИЯ ФИЛЬТРУЕТСЯ ПО СЕРВИСУ И ОПЕРАЦИИ, и это не украшение запроса. Первая
# редакция брала exemplar у метрики без фильтра — то есть у любой из десятков
# серий, включая внутренние спаны и спаны java-backend. Трейс исправно
# находился, но относился к другой операции, и третий критерий честно объявил
# ОТКАЗ. Проверка поймала неверную посылку в себе самой: чтобы утверждать «прыжок
# ведёт в нужный трейс», прыгать надо из ТОЙ САМОЙ точки, про которую утверждаем.
EX_METRIC="traces_span_metrics_duration_milliseconds_bucket{service_name=\"$EX_SERVICE\",span_name=\"$EX_SPAN\"}"

# --- запросы -----------------------------------------------------------------

# Ответ /api/v1/query_exemplars целиком. ВНИМАНИЕ: пишем в stdout и разбираем
# через stdin, без промежуточных файлов. Два соображения, оба проверены на себе:
# `innet -o файл` создаёт файл ВНУТРИ контейнера curl, на хосте его нет; а
# $PYTHON_BIN — это python для Windows, для которого путей вида /tmp/... не
# существует вовсе.
exemplars_json() {
  local start end
  end="$(date +%s)"
  start=$((end - 900))
  innet -s --max-time 20 -G "http://prometheus:9090/api/v1/query_exemplars" \
    --data-urlencode "query=$EX_METRIC" \
    --data-urlencode "start=$start" \
    --data-urlencode "end=$end" 2>/dev/null
}

# --- нагрузка ----------------------------------------------------------------

log "прогон нагрузки: $REQUESTS запросов, seed $SEED"
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED" \
  -concurrency 4 >/dev/null 2>&1 || true

# Exemplars появляются вместе с метрикой, но не мгновенно: нужен скрейп
# гистограммы в формате OpenMetrics. Ждём появления, а не фиксированное время.
log "жду появления exemplars"
EX_COUNT=0
for _ in $(seq 1 20); do
  EX_COUNT="$(exemplars_json | "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)['data']
    print(sum(len(s.get('exemplars', [])) for s in d))
except Exception:
    print(0)
")"
  [ "$EX_COUNT" -gt 0 ] 2>/dev/null && break
  sleep 5
done
echo "  exemplars найдено: $EX_COUNT"

# --- критерий 1: exemplar хранится в Prometheus -------------------------------

if [ "$EX_COUNT" = "0" ]; then
  echo "ОШИБКА: exemplars нет ни одного. Отказало ОДНО ИЗ ТРЁХ условий цепочки," >&2
  echo "        и по метрикам этого не видно — они при этом исправны:" >&2
  echo "          1) prometheus: --enable-feature=exemplar-storage" >&2
  echo "          2) exporters.prometheus: enable_open_metrics: true" >&2
  echo "          3) connectors.spanmetrics: exemplars.enabled: true" >&2
  exit 1
fi

# --- критерий 2: trace_id резолвится в Tempo ----------------------------------

log "беру trace_id из exemplar и ищу трейс в Tempo"
TRACE_ID="$(exemplars_json | "$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)['data']
for s in d:
    for e in s.get('exemplars', []):
        labels = e.get('labels', {})
        # Имя лейбла — trace_id. У Prometheus оно приходит именно так, но в
        # других сборках встречается traceID, поэтому берём оба.
        t = labels.get('trace_id') or labels.get('traceID')
        if t:
            print(t)
            raise SystemExit
")"
echo "  trace_id: ${TRACE_ID:-НЕТ}"

TRACE_JSON="$(innet -s --max-time 20 "http://tempo:3200/api/traces/$TRACE_ID" 2>/dev/null)"
TRACE_BYTES="$(printf '%s' "$TRACE_JSON" | wc -c | tr -d ' ')"
echo "  ответ Tempo: $TRACE_BYTES байт"

# --- критерий 3: трейс тот самый ---------------------------------------------

log "проверяю, что найденный трейс относится к нужной операции"
read -r HAS_SERVICE HAS_SPAN SPAN_COUNT <<<"$(printf '%s' "$TRACE_JSON" | "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
except Exception:
    print('no no 0'); raise SystemExit
batches = d.get('batches') or d.get('data') or []
services, ops, total = set(), set(), 0
for b in batches:
    for a in b.get('resource', {}).get('attributes', []):
        if a.get('key') == 'service.name':
            services.add(a['value'].get('stringValue'))
    for ss in b.get('scopeSpans', []):
        for sp in ss.get('spans', []):
            ops.add(sp.get('name'))
            total += 1
# ASCII, а НЕ 'да'/'нет': сравниваемые значения проходят через кодировку вывода
# python, и на Windows кириллица в них рассыпается — вердикт объявлял ОТКАЗ,
# печатая «??» вместо значения, при полностью исправной цепочке.
print('yes' if '$EX_SERVICE' in services else 'no',
      'yes' if '$EX_SPAN' in ops else 'no',
      total)
")"
echo "  сервис $EX_SERVICE в трейсе: $HAS_SERVICE"
echo "  операция '$EX_SPAN' в трейсе: $HAS_SPAN"
echo "  спанов в трейсе: $SPAN_COUNT"

# --- вердикт -----------------------------------------------------------------

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-44s %s (ожидалось %s)\n" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-44s %s (ожидалось %s)\n" "$name" "$got" "$want"
    FAILED=1
  fi
}

verdict_at_least() {
  local name="$1" got="$2" least="$3"
  if [ "$got" -ge "$least" ] 2>/dev/null; then
    printf "  ГОДЕН   %-44s %s (нужно не меньше %s)\n" "$name" "$got" "$least"
  else
    printf "  ОТКАЗ   %-44s %s (нужно не меньше %s)\n" "$name" "$got" "$least"
    FAILED=1
  fi
}

echo
log "вердикт"
# Точное число exemplars задать нельзя: Prometheus хранит их с ограничением по
# объёму и прикрепляет не к каждому наблюдению. Поэтому здесь «не меньше
# одного» — и это единственный критерий в стенде, где такая форма оправдана
# устройством, а не удобством.
verdict_at_least "exemplars хранятся в Prometheus"    "$EX_COUNT" 1
verdict "trace_id из exemplar непустой"    "$([ -n "$TRACE_ID" ] && echo yes || echo no)" "yes"
verdict_at_least "трейс найден в Tempo, спанов"       "$SPAN_COUNT" 1
verdict "трейс содержит нужный сервис"     "$HAS_SERVICE" "yes"
verdict "трейс содержит нужную операцию"   "$HAS_SPAN" "yes"

echo
if [ "$FAILED" = 0 ]; then
  echo "ИТОГ: прыжок из точки метрики ведёт в осмысленный трейс — все три звена целы."
else
  echo "ИТОГ: цепочка рвётся — смотри строки ОТКАЗ выше."
fi

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Самопроверка exemplars: прыжок metric -> trace"
  echo "=============================================="
  echo "Прогон: $REQUESTS запросов, seed $SEED. Режим стенда: spanmetrics."
  echo "Метрика: $EX_METRIC"
  echo
  echo "Три звена цепочки:"
  echo "  1) exemplars в Prometheus:      $EX_COUNT"
  echo "  2) trace_id из exemplar:        ${TRACE_ID:-НЕТ}"
  echo "     трейс в Tempo:               $TRACE_BYTES байт, спанов $SPAN_COUNT"
  echo "  3) сервис $EX_SERVICE в трейсе: $HAS_SERVICE"
  echo "     операция '$EX_SPAN':         $HAS_SPAN"
  echo
  echo "Условия, без которых цепочка рвётся МОЛЧА (метрики при этом исправны):"
  echo "  - prometheus:  --enable-feature=exemplar-storage"
  echo "  - collector:   exporters.prometheus.enable_open_metrics: true"
  echo "  - collector:   connectors.spanmetrics.exemplars.enabled: true"
  echo "  - grafana:     exemplarTraceIdDestinations в datasource Prometheus"
  echo
  if [ "$FAILED" = 0 ]; then echo "ИТОГ: ГОДЕН"; else echo "ИТОГ: ОТКАЗ"; fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
