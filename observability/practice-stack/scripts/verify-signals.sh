#!/usr/bin/env bash
# Самопроверка стенда: доехали ли ВСЕ ТРИ сигнала до своих бэкендов и сходятся
# ли их числа с планом нагрузки.
#
#   ./scripts/verify-signals.sh [запросов] [seed]
#
# Без этой проверки «стенд работает» — утверждение без доказательства. Критерий
# здесь не «данные появились», а РОВНО СТОЛЬКО, сколько было в плане нагрузки.
#
# Сравнение точное (==), а не «не меньше». Разница принципиальна: «не меньше»
# проходит и при дублировании сигнала, и при постороннем трафике в тот же стенд,
# то есть скрывает как раз те дефекты, которые проверка должна ловить. Стенд
# изолирован, прогон помечен собственным идентификатором — значит точное
# равенство достижимо, и ослаблять критерий незачем.
#
# Значения снимаются ДО и ПОСЛЕ прогона, сверяется дельта. Иначе накопленное
# предыдущими прогонами выдавало бы себя за результат текущего.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

REQUESTS="${1:-200}"
SEED="${2:-42}"
RESULT_FILE="$STAND_DIR/results/01-signals-baseline.txt"

# --- запросы к бэкендам ------------------------------------------------------

prom_value() {
  local query="$1"
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=$query" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    rs = d['data']['result']
    print(int(sum(float(r['value'][1]) for r in rs)) if rs else 0)
except Exception:
    print(-1)
"
}

loki_count() {
  local query="$1"
  innet -s --max-time 20 -G "http://loki:3100/loki/api/v1/query" \
    --data-urlencode "query=$query" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    rs = d['data']['result']
    print(int(sum(float(r['value'][1]) for r in rs)) if rs else 0)
except Exception:
    print(-1)
"
}

# Tempo ищет ТОЛЬКО в заданном окне времени: без start/end поиск возвращает
# пустой результат даже при доехавших трейсах — проверено, легко принять за
# потерю данных.
tempo_count() {
  local traceql="$1" now start end
  now="$(date +%s)"
  start=$((now - 3600))
  end=$((now + 120))
  innet -s --max-time 25 -G "http://tempo:3200/api/search" \
    --data-urlencode "q=$traceql" \
    --data-urlencode "start=$start" --data-urlencode "end=$end" \
    --data-urlencode "limit=1000" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(len(d.get('traces') or []))
except Exception:
    print(-1)
"
}

# Ждать, пока значение не достигнет ожидаемого. Точное ожидание вместо
# фиксированного sleep: batch-процессоры и периодический reader отдают данные с
# задержкой, и «подождать пять секунд» либо мало (ложная потеря), либо много
# (медленная проверка).
wait_for() {
  local what="$1" expected="$2" getter="$3" arg="$4" tries="${5:-30}" i=0 value=0
  while [ "$i" -lt "$tries" ]; do
    value="$($getter "$arg")"
    if [ "$value" -ge "$expected" ] 2>/dev/null; then
      echo "$value"
      return 0
    fi
    i=$((i + 1))
    sleep 2
  done
  echo "$value"
  return 1
}

# --- план --------------------------------------------------------------------

log "снимаю значения ДО прогона"
BEFORE_ORDERS="$(prom_value 'sum(khorost_tech_orders_created_total)')"
BEFORE_LOGS="$(loki_count "sum(count_over_time({service_name=\"go-frontend\"} |= \`заказ создан\` [24h]))")"
echo "  метрика заказов: $BEFORE_ORDERS, логов о заказах: $BEFORE_LOGS"

# Окно времени и метка прогона. Окно нужно потому, что поиск Tempo без start/end
# возвращает пусто; метка — потому что окна НЕДОСТАТОЧНО: поиск округляет
# границы до блоков и захватывает соседние прогоны (проверено: прогон на 100
# запросов давал 200 трейсов). Метка едет заголовком X-Load-Run и попадает в
# атрибут спана load.run, поэтому счёт получается ровно по своему прогону.
WINDOW_START="$(( $(date +%s) - 5 ))"
RUN_ID="run-$(date +%s)"

log "прогон нагрузки: $REQUESTS запросов, seed $SEED"
LOAD_OUT="$STAND_DIR/results/.last-load.json"
rm -f "$LOAD_OUT"
# Итог забирается из stdout, а не из файла внутри контейнера: путь, переданный
# аргументом из Git Bash, MSYS переписывает в путь Windows (/out/x.json ->
# C:/Program Files/Git/out/x.json), и генератор рапортует «итог не записан» при
# формально успешном прогоне. MSYS_NO_PATHCONV этого не отменял — обмен через
# stdout снимает вопрос совсем: перенаправление делает хост.
# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED" \
  -concurrency 4 -run "$RUN_ID" -json 2>/dev/null | sed -n '/^{/,$p' > "$LOAD_OUT" || true

if [ ! -f "$LOAD_OUT" ]; then
  echo "ОШИБКА: генератор не записал итог, сверять нечего" >&2
  exit 1
fi

# Успешных заказов ожидается столько, сколько в плане ответов 201: именно они
# создают событие, метрику и запись лога.
# Файл передаётся python через stdin, а не путём: $STAND_DIR — это MSYS-путь
# вида /g/7/..., и python для Windows такой путь открыть не может.
EXPECTED_OK="$("$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
print(d['expected_by_status'].get('201', 0))
" < "$LOAD_OUT")"
ACTUAL_OK="$("$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
print(d['actual_by_status'].get('201', 0))
" < "$LOAD_OUT")"
EXPECTED_ERR="$("$PYTHON_BIN" -c "
import sys, json
a = json.load(sys.stdin)['expected_by_status']
print(a.get('404', 0) + a.get('502', 0))
" < "$LOAD_OUT")"
EXPECTED_TOTAL=$((EXPECTED_OK + EXPECTED_ERR))
echo "  план: 201 = $EXPECTED_OK, ошибок = $EXPECTED_ERR; факт 201 = $ACTUAL_OK"

if [ "$EXPECTED_OK" != "$ACTUAL_OK" ]; then
  echo "ОШИБКА: сам прогон не совпал с планом — проверять сигналы бессмысленно" >&2
  exit 1
fi

# --- три сигнала -------------------------------------------------------------

log "метрики: жду khorost_tech_orders_created_total >= $((BEFORE_ORDERS + EXPECTED_OK))"
rc=0
AFTER_ORDERS="$(wait_for "метрика" $((BEFORE_ORDERS + EXPECTED_OK)) prom_value 'sum(khorost_tech_orders_created_total)')" || rc=$?
DELTA_ORDERS=$((AFTER_ORDERS - BEFORE_ORDERS))

# Отрицательная дельта означает не потерю сигнала, а СБРОС СЧЁТЧИКА: метрика
# кумулятивная и при рестарте приложения начинается с нуля. Проверено на стенде —
# ряд шёл 18520, 18520, 18520, затем 186 сразу после пересоздания контейнеров, а
# проверка рапортовала «НЕДОСТАЧА: сигнал потерян» при полностью исправном
# конвейере.
#
# Если сброс случился ДО прогона (обычный случай: стенд только что поднят), то
# счётчик считает ровно этот прогон, и правильная дельта — само значение AFTER.
if [ "$DELTA_ORDERS" -lt 0 ] 2>/dev/null; then
  echo "  ВНИМАНИЕ: счётчик сброшен ($BEFORE_ORDERS -> $AFTER_ORDERS)."
  echo "            Это рестарт приложения, а не потеря сигнала: метрика кумулятивная."
  echo "            Дельта считается от нуля. Если рестарт произошёл ВО ВРЕМЯ прогона,"
  echo "            число окажется заниженным — тогда прогоните проверку заново."
  DELTA_ORDERS="$AFTER_ORDERS"
fi
echo "  метрика: было $BEFORE_ORDERS, стало $AFTER_ORDERS, дельта $DELTA_ORDERS"

log "логи: жду записи «заказ создан» в Loki"
rc2=0
AFTER_LOGS="$(wait_for "логи" $((BEFORE_LOGS + EXPECTED_OK)) loki_count "sum(count_over_time({service_name=\"go-frontend\"} |= \`заказ создан\` [24h]))")" || rc2=$?
DELTA_LOGS=$((AFTER_LOGS - BEFORE_LOGS))
echo "  логи: было $BEFORE_LOGS, стало $AFTER_LOGS, дельта $DELTA_LOGS"

# Спан create_order создаётся на КАЖДЫЙ запрос, включая ошибочные: он стартует
# до обращения к складу. Поэтому сверка идёт с общим числом запросов, а не с
# числом успешных.
log "трейсы: считаю в Tempo спаны create_order за окно прогона"
rc3=0
TRACES="$(wait_for "трейсы" "$EXPECTED_TOTAL" tempo_count "{name=\"create_order\" && span.load.run=\"$RUN_ID\"}")" || rc3=$?
echo "  трейсов с create_order: $TRACES"

log "трейсы с ошибкой: должны быть ровно ошибочные запросы"
ERROR_TRACES="$(tempo_count "{name=\"create_order\" && span.load.run=\"$RUN_ID\" && status=error}")"
echo "  трейсов create_order со статусом error: $ERROR_TRACES"

# Критерий корреляции точный и в другую сторону: записей о заказе БЕЗ trace_id
# должно быть ноль. Считать записи С trace_id бессмысленно — накопленное за
# прошлые прогоны само по себе даёт большое число.
log "корреляция: записей о заказах без trace_id должно быть 0"
UNCORRELATED="$(loki_count "sum(count_over_time({service_name=\"go-frontend\"} |= \`заказ создан\` | trace_id = \"\" [24h]))")"
echo "  логов о заказах с ПУСТЫМ trace_id: $UNCORRELATED"

# --- вердикт -----------------------------------------------------------------

FAILED=0
# Точное равенство, а не «не меньше». Если получено БОЛЬШЕ ожидаемого — это тоже
# отказ: значит сигнал продублировался либо в стенд пришёл посторонний трафик.
# Обе причины надо увидеть, а не списать на «ну, не меньше же».
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-28s %s (ожидалось ровно %s)\n" "$name" "$got" "$want"
  elif [ "$got" -gt "$want" ] 2>/dev/null; then
    printf "  ОТКАЗ   %-28s %s (ожидалось ровно %s — ЛИШНЕЕ: дубли или посторонний трафик)\n" "$name" "$got" "$want"
    FAILED=1
  else
    printf "  ОТКАЗ   %-28s %s (ожидалось ровно %s — НЕДОСТАЧА: сигнал потерян)\n" "$name" "$got" "$want"
    FAILED=1
  fi
}

# Отдельная проверка «должно быть ровно ноль»: для корреляции годен только
# нулевой результат, «не меньше» тут ничего не значит.
verdict_zero() {
  local name="$1" got="$2"
  if [ "$got" = "0" ]; then
    printf "  ГОДЕН   %-28s %s
" "$name" "$got"
  else
    printf "  ОТКАЗ   %-28s %s (должно быть 0)
" "$name" "$got"
    FAILED=1
  fi
}

echo
log "вердикт"
verdict "метрика заказов (дельта)" "$DELTA_ORDERS" "$EXPECTED_OK"
verdict "логи о заказах (дельта)" "$DELTA_LOGS" "$EXPECTED_OK"
verdict "трейсы create_order" "$TRACES" "$EXPECTED_TOTAL"
verdict "трейсы с ошибкой" "$ERROR_TRACES" "$EXPECTED_ERR"
verdict_zero "логи без trace_id" "$UNCORRELATED"

{
  echo "Самопроверка сигналов, $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo "==============================================="
  echo "Прогон: $REQUESTS запросов, seed $SEED, метка $RUN_ID"
  echo "Ожидалось успешных $EXPECTED_OK, ошибочных $EXPECTED_ERR, всего $EXPECTED_TOTAL"
  echo
  echo "СИГНАЛ                        ПОЛУЧЕНО   ОЖИДАЛОСЬ   СРАВНЕНИЕ"
  printf "метрика заказов (дельта)      %8s   %9s   ровно
" "$DELTA_ORDERS" "$EXPECTED_OK"
  printf "логи о заказах (дельта)       %8s   %9s   ровно
" "$DELTA_LOGS" "$EXPECTED_OK"
  printf "трейсы create_order           %8s   %9s   ровно
" "$TRACES" "$EXPECTED_TOTAL"
  printf "трейсы со статусом error      %8s   %9s   ровно
" "$ERROR_TRACES" "$EXPECTED_ERR"
  printf "логи БЕЗ trace_id             %8s   %9s   ровно
" "$UNCORRELATED" "0"
  echo
  echo "Спан create_order создаётся на КАЖДЫЙ запрос, включая ошибочные (он стартует"
  echo "до обращения к складу), поэтому его ожидание — общее число запросов, а не"
  echo "число успешных. Метрика и запись лога появляются только у успешных заказов."
  echo
  if [ "$FAILED" = "0" ]; then
    echo "ИТОГ: все три сигнала доехали, числа сходятся с планом."
  else
    echo "ИТОГ: ОТКАЗ — см. строки выше."
  fi
} > "$RESULT_FILE"

echo
echo "записано: results/01-signals-baseline.txt"
exit "$FAILED"
