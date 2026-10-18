#!/usr/bin/env bash
# Принимает ли Loki часть запроса, на который ответил 429.
#
#   ./scripts/verify-partial-accept.sh
#
# ЗАЧЕМ. В замере изоляции число событий, доставленных синком Vector, оказалось
# МЕНЬШЕ, чем видно в Loki: 2874 против 5000. Напрашивалось объяснение «у
# Vector единица учёта батч, у Loki строка» — но vector_component_sent_events_total
# считает СОБЫТИЯ, а не запросы, так что само по себе оно ничего не объясняет.
#
# Настоящий вопрос: сохраняет ли Loki строки из запроса, на который ответил
# ошибкой. Если да — расхождение объясняется без всякой разницы единиц: часть
# событий осела в Loki, а Vector считает их недоставленными, потому что запрос
# завершился ошибкой.
#
# Опыт прямой, без Vector: забиваем предел потоков, затем шлём ОДИН запрос из
# двух потоков — существующего и нового. Ответ будет 429, а дальше смотрим,
# появилась ли запись в существующем потоке.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

RESULT_FILE="$STAND_DIR/results/07-partial-accept.txt"
LIMIT="${LIMIT:-5000}"       # max_global_streams_per_user, значение по умолчанию
BATCH="${BATCH:-500}"

push() {
  # Тело запроса идёт со стандартного ввода, поэтому контейнеру нужен -i:
  # innet из lib.sh запускает docker run без него, и `--data-binary @-`
  # читает EOF. Loki отвечает 400, и выглядит это как ошибка формата.
  docker run --rm -i --network "$NETWORK" curlimages/curl:8.11.1 \
    -s -o /dev/null -w '%{http_code}' --max-time 60 \
    -X POST "http://loki:3100/loki/api/v1/push" \
    -H 'Content-Type: application/json' --data-binary @- 2>/dev/null
}

# Сколько записей лежит в конкретном потоке.
count_stream() {
  local sel="$1" now start
  now="$(date +%s)"; start=$((now - 3600))
  innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range" \
    --data-urlencode "query=count_over_time(${sel}[1h])" \
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

log "шаг 1: занимаем предел активных потоков ($LIMIT)"
NOW_NS="$(( $(date +%s) * 1000000000 ))"
created=0
# Идём С ЗАПАСОМ: предел считается по АКТИВНЫМ потокам, часть успевает
# устареть, поэтому ровно LIMIT созданных потоков отказа ещё не дают.
while [ "$created" -lt "$(( LIMIT * 3 ))" ]; do
  code="$("$PYTHON_BIN" -c "
import json, sys
start = $created
ns = $NOW_NS
streams = []
for i in range(start, start + $BATCH):
    streams.append({
        'stream': {'probe': 'fill', 'id': 's%d' % i},
        'values': [[str(ns + i), 'fill']],
    })
sys.stdout.write(json.dumps({'streams': streams}))
" | push)"
  created=$(( created + BATCH ))
  [ "$code" = "429" ] && break
done
echo "  создано потоков: до $created, последний ответ: $code"

if [ "$code" != "429" ]; then
  echo "ОШИБКА: предел не достигнут — опыт не состоялся, ответ $code." >&2
  exit 1
fi

# Поток, который ТОЧНО существует: он создан на первой партии.
EXIST='{probe="fill", id="s0"}'
BEFORE="$(count_stream "$EXIST")"
echo "  в существующем потоке до опыта: $BEFORE"

log "шаг 2: один запрос из двух потоков — существующего и нового"
TS="$(( $(date +%s) * 1000000000 + 999 ))"
CODE2="$("$PYTHON_BIN" -c "
import json, sys
ts = $TS
sys.stdout.write(json.dumps({'streams': [
    {'stream': {'probe': 'fill', 'id': 's0'},        'values': [[str(ts), 'partial-probe']]},
    {'stream': {'probe': 'fill', 'id': 'brand-new'}, 'values': [[str(ts), 'partial-probe']]},
]}))
" | push)"
echo "  ответ на смешанный запрос: $CODE2"

sleep 10
AFTER="$(count_stream "$EXIST")"
NEWCNT="$(count_stream '{probe="fill", id="brand-new"}')"
echo "  в существующем потоке после: $AFTER (было $BEFORE)"
echo "  в новом потоке: $NEWCNT"

# Опыт подтверждён ТОЛЬКО при выполнении всех трёх условий сразу. Проверка
# «429 и существующий поток вырос» недостаточна: если бы Loki принял и новый
# поток тоже, это означало бы, что предел не действует, — то есть опыт мерил бы
# не то, а вердикт всё равно вышел бы положительным.
VERDICT="no"
if [ "$CODE2" = "429" ] && [ "$AFTER" -gt "$BEFORE" ] && [ "$NEWCNT" = "0" ]; then
  VERDICT="yes"
fi

mkdir -p "$STAND_DIR/results"
{
  echo "Частичный приём Loki: сохраняет ли он строки из запроса, отвергнутого с 429"
  echo "=========================================================================="
  echo
  echo "ПОСТАНОВКА"
  echo "Предел активных потоков занят (max_global_streams_per_user = $LIMIT), затем"
  echo "отправлен ОДИН запрос из двух потоков: существующего и нового."
  echo
  echo "  ответ на смешанный запрос:            $CODE2"
  echo "  записей в существующем потоке до:     $BEFORE"
  echo "  записей в существующем потоке после:  $AFTER"
  echo "  записей в новом потоке:               $NEWCNT"
  echo
  if [ "$VERDICT" = "yes" ]; then
    echo "ВЫВОД: ДА, приём частичный. Запрос завершился ошибкой $CODE2, но строка в"
    echo "существующий поток сохранена, а строка в новый — отвергнута."
    echo
    echo "СЛЕДСТВИЕ ДЛЯ ЗАМЕРА ИЗОЛЯЦИИ. Расхождение «доставлено синком» (2874) и"
    echo "«видно в Loki» (5000) объясняется без всякой разницы единиц учёта:"
    echo "vector_component_sent_events_total считает СОБЫТИЯ, и события из"
    echo "запросов, завершившихся ошибкой, в него не попадают — хотя часть из них"
    echo "Loki сохранил. Отправитель считает их недоставленными, получатель их"
    echo "хранит."
    echo
    echo "ПРАКТИЧЕСКОЕ СЛЕДСТВИЕ: по счётчику успешной отправки нельзя судить о"
    echo "том, что лежит в бэкенде, — ни в большую, ни в меньшую сторону."
  else
    echo "ОТКАЗ: частичный приём НЕ ПОДТВЕРЖДЁН."
    echo "  ответ на смешанный запрос: $CODE2 (ожидалось 429)"
    echo "  существующий поток: было $BEFORE, стало $AFTER (ожидался рост)"
    echo "  новый поток: $NEWCNT (ожидался 0)"
    echo
    echo "Любое из трёх несовпадений обесценивает опыт. Если новый поток НЕ пуст,"
    echo "предел не действовал и запрос не был отвергнут по кардинальности —"
    echo "то есть проверялось не то. Вывод о частичном приёме, на который"
    echo "опирается объяснение расхождения в 06-isolation, в таком случае"
    echo "НЕДОКАЗАН."
  fi
} > "$RESULT_FILE"

echo
echo "частичный приём: $VERDICT"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

# ЭТО ПРОВЕРКА, А НЕ НАБЛЮДЕНИЕ, поэтому неподтверждённый опыт — отказ. Иначе
# регрессия Loki или поломка стенда дали бы зелёный запуск и молча переписали
# бы evidence отчётом, который ничего не доказывает.
if [ "$VERDICT" != "yes" ]; then
  echo "ОТКАЗ: частичный приём не подтверждён — вывод, опирающийся на него, недоказан." >&2
  exit 1
fi
exit 0
