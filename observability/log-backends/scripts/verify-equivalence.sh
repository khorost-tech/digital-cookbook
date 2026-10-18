#!/usr/bin/env bash
# Сопоставимость трёх бэкендов: доехало ли одно и то же и отвечают ли они
# одинаково на один и тот же вопрос.
#
#   ./scripts/verify-equivalence.sh [записей]
#
# ЭТА ПРОВЕРКА ИДЁТ ПЕРВОЙ И СТРАХУЕТ ВСЕ ОСТАЛЬНЫЕ. Пока не доказано, что во
# все три бэкенда приехал один и тот же набор записей, мерить объём и ресурсы
# бессмысленно: числа будут описывать разные данные, а разницу спишут на модели
# индексации.
#
# Вопрос один: сколько записей уровня error у сервиса checkout. Ответ известен
# заранее — генератор кладёт его в манифест ДО отправки.
#
# ЗАПРОСЫ РАЗНЫЕ ПО НЕОБХОДИМОСТИ, а не по небрежности: одна и та же запись
# приезжает в три бэкенда с РАЗНЫМИ именами полей. Переносимого запроса не
# существует на уровне модели данных, а не только грамматики.
#
# НАБОР ПОЛЕЙ СНИМАЕТСЯ ПРОГОНОМ, А НЕ БЕРЁТСЯ ИЗ ПАМЯТИ. Ранняя проверка
# выполнимости (results/00-feasibility.txt) снимала его при ПРЯМОМ push в Loki и
# зафиксировала там метку detected_level. В конвейере этого стенда доставка идёт
# синком Vector, и набор другой. Перенос того вывода сюда был ошибкой: набор
# меток зависит от СПОСОБА ДОСТАВКИ, поэтому ниже он запрашивается у живых
# бэкендов и попадает в отчёт как замер.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

COUNT="${1:-20000}"
SEED="${2:-42}"
RUN="eq-$(date +%s)"
RESULT_FILE="$STAND_DIR/results/01-equivalence.txt"

# --- нагрузка ----------------------------------------------------------------

log "прогон: $COUNT записей, seed $SEED, метка $RUN"
docker compose --profile tools run --rm -T loadgen \
  -count "$COUNT" -seed "$SEED" -run "$RUN" >/dev/null 2>&1

# Манифест лежит в томе logs, а на хосте его нет — читаем через контейнер.
MANIFEST="$(docker run --rm -v log-backends_logs:/logs alpine:3.21 cat /logs/manifest.json 2>/dev/null)"
read -r FACT_TOTAL FACT_ERRORS <<<"$(printf '%s' "$MANIFEST" | "$PYTHON_BIN" -c "
import sys, json
m = json.load(sys.stdin)
print(m['total'], m['errors_checkout'])
")"
echo "  отправлено: $FACT_TOTAL, из них error у checkout: $FACT_ERRORS"

# --- ожидание доставки -------------------------------------------------------
#
# Ждём ИЗВЕСТНОГО числа, а не фиксированное время. Пауза «на всякий случай»
# рано или поздно оказывается недостаточной, и тогда проверка обвиняет
# исправный бэкенд в потере данных.

count_loki() {
  local now start
  now="$(date +%s)"
  start=$((now - 7200))
  innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range" \
    --data-urlencode 'query=sum(count_over_time({service=~".+"}[2h]))' \
    --data-urlencode "start=${start}000000000" \
    --data-urlencode "end=${now}000000000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    r = json.load(sys.stdin)['data']['result']
    print(int(float(r[0]['values'][-1][1])) if r else 0)
except Exception:
    print(0)
"
}

count_os() {
  innet -s --max-time 30 "http://opensearch:9200/logs-default/_count" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(int(json.load(sys.stdin).get('count', 0)))
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

log "жду доставки всех $FACT_TOTAL записей в три бэкенда"
N_LOKI=0; N_OS=0; N_VL=0
for _ in $(seq 1 40); do
  N_LOKI="$(count_loki)"; N_OS="$(count_os)"; N_VL="$(count_vl)"
  if [ "$N_LOKI" -ge "$FACT_TOTAL" ] && [ "$N_OS" -ge "$FACT_TOTAL" ] && [ "$N_VL" -ge "$FACT_TOTAL" ] 2>/dev/null; then
    break
  fi
  sleep 5
done
echo "  доехало: Loki $N_LOKI, OpenSearch $N_OS, VictoriaLogs $N_VL"

# --- один вопрос, три языка --------------------------------------------------

log "один вопрос на трёх языках: error у checkout"

# LogQL. Метки service и level заданы в конфиге Vector, поэтому фильтр идёт по
# ним — самый дешёвый путь в модели Loki.
Q_LOKI='sum(count_over_time({service="checkout", level="error"}[2h]))'
NOW="$(date +%s)"
A_LOKI="$(innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range" \
  --data-urlencode "query=$Q_LOKI" \
  --data-urlencode "start=$(( NOW - 7200 ))000000000" \
  --data-urlencode "end=${NOW}000000000" 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    r = json.load(sys.stdin)['data']['result']
    print(int(float(r[0]['values'][-1][1])) if r else 0)
except Exception:
    print(0)
")"

# DSL. Поля в OpenSearch называются так же, как их отправили, но у строковых
# полей динамический маппинг создаёт подполе .keyword — точное совпадение ищется
# по нему, а не по проанализированному тексту.
Q_OS='{"query":{"bool":{"filter":[{"term":{"service.keyword":"checkout"}},{"term":{"level.keyword":"error"}}]}}}'
A_OS="$(innet -s --max-time 30 -X POST "http://opensearch:9200/logs-default/_count" \
  -H 'Content-Type: application/json' -d "$Q_OS" 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    print(int(json.load(sys.stdin).get('count', 0)))
except Exception:
    print(0)
")"

# LogsQL. Синтаксис третий, а поля те же, что отправлены.
Q_VL='service:="checkout" AND level:="error" | count()'
A_VL="$(innet -s --max-time 30 -X POST "http://victorialogs:9428/select/logsql/query" \
  -d "query=$Q_VL" 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    line = sys.stdin.readline().strip()
    print(int(json.loads(line)['count(*)']) if line else 0)
except Exception:
    print(0)
")"

echo "  LogQL:  $A_LOKI"
echo "  DSL:    $A_OS"
echo "  LogsQL: $A_VL"

# --- фактический набор полей -------------------------------------------------
#
# Не «как отправили», а «что лежит в бэкенде после доставки». Именно это число
# полей потом объясняет разницу объёмов и ловушку кардинальности.

log "фактический набор полей после доставки"

F_LOKI="$(innet -s --max-time 30 "http://loki:3100/loki/api/v1/labels" 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    print(', '.join(sorted(json.load(sys.stdin).get('data', []))))
except Exception:
    print('')
")"

F_OS="$(innet -s --max-time 30 "http://opensearch:9200/logs-default/_mapping" 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    idx = next(iter(d.values()))
    print(', '.join(sorted(idx['mappings']['properties'].keys())))
except Exception:
    print('')
")"

F_VL="$(innet -s --max-time 30 -X POST "http://victorialogs:9428/select/logsql/field_names" -d 'query=*' 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(', '.join(sorted(v['value'] for v in d.get('values', []) if v.get('value'))))
except Exception:
    print('')
")"

echo "  Loki (метки):       $F_LOKI"
echo "  OpenSearch (поля):  $F_OS"
echo "  VictoriaLogs:       $F_VL"

# --- вердикт -----------------------------------------------------------------

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-42s %s (ожидалось %s)\n" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-42s %s (ожидалось %s)\n" "$name" "$got" "$want"
    FAILED=1
  fi
}

echo
log "вердикт"
# Сначала полнота: доехало ли всё. Потери ловятся здесь, а не в замере объёма,
# где они выглядели бы как «этот бэкенд компактнее».
verdict "Loki: записей доехало"         "$N_LOKI" "$FACT_TOTAL"
verdict "OpenSearch: записей доехало"   "$N_OS"   "$FACT_TOTAL"
verdict "VictoriaLogs: записей доехало" "$N_VL"   "$FACT_TOTAL"
# Затем смысл: отвечают ли три языка одно и то же, и совпадает ли это с фактом.
verdict "LogQL = факт"                  "$A_LOKI" "$FACT_ERRORS"
verdict "DSL = факт"                    "$A_OS"   "$FACT_ERRORS"
verdict "LogsQL = факт"                 "$A_VL"   "$FACT_ERRORS"
# Набор меток Loki — точное равенство, а не «содержит». Ослабленная проверка
# пропустила бы и лишнюю метку, и потерю заданной, а обе меняют выводы о
# кардинальности и объёме.
verdict "Loki: метки после доставки"    "$F_LOKI" "level, service, service_name"

echo
if [ "$FAILED" = 0 ]; then
  echo "ИТОГ: во все три бэкенда приехало одно и то же, и три языка отвечают одинаково."
  echo "      Остальные замеры этого стенда достоверны."
else
  echo "ИТОГ: сопоставимость НЕ доказана — остальные замеры недостоверны."
fi

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Сопоставимость трёх бэкендов: один вопрос на трёх языках"
  echo "========================================================"
  echo "Прогон: $COUNT записей, seed $SEED, метка $RUN."
  echo
  echo "Факт из манифеста генератора (посчитан ДО отправки):"
  echo "  всего записей:            $FACT_TOTAL"
  echo "  error у сервиса checkout: $FACT_ERRORS"
  echo
  echo "ДОЕХАЛО"
  echo "  Loki:          $N_LOKI"
  echo "  OpenSearch:    $N_OS"
  echo "  VictoriaLogs:  $N_VL"
  echo
  echo "ОДИН ВОПРОС, ТРИ ЯЗЫКА"
  echo "  LogQL:  $Q_LOKI"
  echo "    ответ: $A_LOKI"
  echo "  DSL:    $Q_OS"
  echo "    ответ: $A_OS"
  echo "  LogsQL: $Q_VL"
  echo "    ответ: $A_VL"
  echo
  echo "ФАКТИЧЕСКИЙ НАБОР ПОЛЕЙ ПОСЛЕ ДОСТАВКИ (снят этим прогоном)"
  echo "  Loki (метки):      $F_LOKI"
  echo "  OpenSearch (поля): $F_OS"
  echo "  VictoriaLogs:      $F_VL"
  echo
  echo "ПОЧЕМУ ЗАПРОСЫ РАЗНЫЕ НЕ ТОЛЬКО СИНТАКСИСОМ"
  echo "OpenSearch сохраняет поля как отправлено; VictoriaLogs переименовывает"
  echo "служебные в _msg и _time; Loki добавляет метку service_name, которой в"
  echo "записи не было — он выводит её из service. Плюс у OpenSearch точное"
  echo "совпадение по строке ищется через подполе .keyword, которое создаёт"
  echo "динамический маппинг. Переносимость запросов отсутствует на уровне"
  echo "МОДЕЛИ ДАННЫХ, а не только грамматики."
  echo
  echo "МЕТКИ ЗАВИСЯТ ОТ СПОСОБА ДОСТАВКИ, А НЕ ТОЛЬКО ОТ КОНФИГУРАЦИИ LOKI."
  echo "При ПРЯМОМ push (results/00-feasibility.txt) Loki добавлял ещё и"
  echo "detected_level — уровень, выведенный из содержимого. Через синк Vector"
  echo "этой метки нет: выше снят фактический набор, и её там не видно."
  echo "Практическое следствие: прежде чем отключать «лишнюю» метку в конфиге,"
  echo "стоит посмотреть, приходит ли она вообще в вашем конвейере."
  echo
  if [ "$FAILED" = 0 ]; then echo "ИТОГ: ГОДЕН"; else echo "ИТОГ: ОТКАЗ"; fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
