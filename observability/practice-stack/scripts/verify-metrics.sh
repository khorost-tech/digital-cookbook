#!/usr/bin/env bash
# Самопроверка метрик RED/USE: считают ли правила записи то, что должны, и
# сходятся ли их числа с фактическим прогоном нагрузки.
#
#   ./scripts/verify-metrics.sh [запросов] [seed]
#
# Проверка отвечает на вопрос, который «зелёный» дашборд не решает: правило может
# успешно вычисляться и при этом возвращать ПУСТОТУ. В API Prometheus у такого
# правила health="ok" — оно не упало, просто данных нет. Панель покажет «No data»,
# алерт поверх него не сработает никогда, и обнаружится это в тот момент, когда
# алерт был нужен. Поэтому здесь проверяется не здоровье правил, а наличие
# конкретных значений и их совпадение с планом нагрузки.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

REQUESTS="${1:-200}"
SEED="${2:-42}"
RESULT_FILE="$STAND_DIR/results/07-metrics.txt"

require_tools

# --- запросы к Prometheus ----------------------------------------------------

# Возвращает число серий в ответе. Нужно отдельно от значения: «серий 0» и
# «значение 0» — разные диагнозы, и путать их нельзя.
prom_series() {
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=$1" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(len(json.load(sys.stdin)['data']['result']))
except Exception:
    print(-1)
"
}

# Сумма значений по всем сериям. Пустой ответ отдаётся как EMPTY, а не как 0:
# иначе отсутствие правила неотличимо от честного нуля.
prom_sum() {
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=$1" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    rs = json.load(sys.stdin)['data']['result']
    print(round(sum(float(r['value'][1]) for r in rs), 6) if rs else 'EMPTY')
except Exception:
    print('ERR')
"
}

# Число серий с КОНЕЧНЫМ значением и список сервисов, у которых значение не
# конечно. Отдельно от prom_series, и разница здесь принципиальна: серия может
# существовать, а значение быть NaN — тогда `count()` по ней покажет полное
# покрытие, а алерт поверх такого правила молчал бы.
#
# Именно так проверка и обманулась: сервисов в правиле было столько же, сколько в
# исходной метрике, критерий проходил, а java-backend отдавал NaN и по доле
# ошибок, и по p95. Ловится только проверкой конечности каждого значения.
prom_finite() {
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query"     --data-urlencode "query=$1" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json, math
try:
    rs = json.load(sys.stdin)['data']['result']
except Exception:
    print('-1 ERR'); raise SystemExit
finite, bad = 0, []
for r in rs:
    try:
        v = float(r['value'][1])
    except Exception:
        v = float('nan')
    if math.isfinite(v):
        finite += 1
    else:
        bad.append('%s=%s' % (r['metric'].get('service_name', '?'), r['value'][1]))
print('%d %s' % (finite, ','.join(bad) if bad else '-'))
"
}

# Значения правила в разрезе service_name — «service=value» через пробел.
prom_by_service() {
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=$1" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    rs = json.load(sys.stdin)['data']['result']
    print(' '.join('%s=%s' % (r['metric'].get('service_name','-'), round(float(r['value'][1]),6))
                   for r in sorted(rs, key=lambda x: x['metric'].get('service_name',''))) or 'EMPTY')
except Exception:
    print('ERR')
"
}

# Здоровье правил по API. health="ok" здесь НЕ считается доказательством работы —
# только отсутствие ошибки вычисления. Проверяется отдельно именно потому, что
# соблазн принять его за доказательство велик.
rules_health() {
  innet -s --max-time 15 "http://prometheus:9090/api/v1/rules" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    gs = json.load(sys.stdin)['data']['groups']
    bad = [r['name'] for g in gs for r in g['rules'] if r.get('health') != 'ok']
    total = sum(len(g['rules']) for g in gs)
    print('%d %d %s' % (total, len(bad), ','.join(bad) if bad else '-'))
except Exception:
    print('-1 -1 ERR')
"
}

wait_nonempty() {
  local query="$1" tries="${2:-30}" i=0 v="EMPTY"
  while [ "$i" -lt "$tries" ]; do
    v="$(prom_sum "$query")"
    if [ "$v" != "EMPTY" ] && [ "$v" != "ERR" ]; then
      echo "$v"
      return 0
    fi
    i=$((i + 1))
    sleep 2
  done
  echo "$v"
  return 1
}

# --- 0. правила загружены ----------------------------------------------------

log "правила записи: загружены ли и вычисляются ли без ошибок"
read -r RULES_TOTAL RULES_BAD RULES_BAD_NAMES <<<"$(rules_health)"
echo "  правил всего: $RULES_TOTAL, с ошибкой вычисления: $RULES_BAD ($RULES_BAD_NAMES)"

if [ "$RULES_TOTAL" -le 0 ] 2>/dev/null; then
  echo "ОШИБКА: Prometheus не видит ни одного правила. Проверь rule_files в" >&2
  echo "        prometheus.yml и что каталог prometheus/rules примонтирован." >&2
  exit 1
fi

# --- 0.5. ПРОГРЕВ ------------------------------------------------------------
#
# Обязателен на чистом стенде, и это не перестраховка.
#
# Сразу после `up.sh all` метрики `http_server_request_duration_seconds_count`
# НЕ СУЩЕСТВУЕТ вовсе — ни у одного сервиса (проверено запросом сразу после
# подъёма: «нет ни одной»). Java-агент экспортирует cumulative-гистограмму только
# после первых обслуженных запросов, а `rate()` нужны минимум ДВЕ точки скрейпа:
# по одной точке производную не посчитать. Поэтому первый же измеряемый прогон на
# холодном стенде давал java-backend=NaN у доли ошибок и у p95 — и, как следствие,
# ложный critical от алерта на отсутствие трафика (тогда он ещё требовал
# HTTP-rate от каждого сервиса; сейчас это ServiceDown и NoTrafficAtEntrypoint).
#
# Прогрев закрывает это явно: гоняем небольшую нагрузку и ЖДЁМ, пока у всех
# ожидаемых сервисов появится серия И станет конечным rate по ней. Только после
# этого начинается измеряемая часть.
log "прогрев: создаю серии и жду, пока rate по ним станет конечным"
docker compose run --rm --no-deps -T loadgen   -target http://go-frontend:8080 -requests 120 -seed 1 -concurrency 4 >/dev/null 2>&1 || true

WARM_LIMIT="${WARM_LIMIT:-40}"
warm_i=0
WARM_SERVICES=0
WARM_FINITE=0
while [ "$warm_i" -lt "$WARM_LIMIT" ]; do
  WARM_SERVICES="$(prom_series 'count by (service_name) (http_server_request_duration_seconds_count)')"
  read -r WARM_FINITE _ <<<"$(prom_finite 'job:http_server_requests:rate5m')"
  # Ждём ровно то, что потом проверяем: серии есть у обоих сервисов И rate по
  # каждому конечен. Ожидание «серия появилась» недостаточно — по одной точке
  # rate ещё NaN.
  if [ "$WARM_SERVICES" -ge 2 ] 2>/dev/null && [ "$WARM_FINITE" -ge 2 ] 2>/dev/null; then
    break
  fi
  # Догружаем, иначе окно rate опустеет раньше, чем появится вторая точка.
  docker compose run --rm --no-deps -T loadgen     -target http://go-frontend:8080 -requests 40 -seed 1 -concurrency 2 >/dev/null 2>&1 || true
  warm_i=$((warm_i + 1))
  sleep 5
done
echo "  сервисов с серией: $WARM_SERVICES, из них с конечным rate: $WARM_FINITE (ждали $((warm_i * 5))с)"

if [ "$WARM_SERVICES" -lt 2 ] 2>/dev/null || [ "$WARM_FINITE" -lt 2 ] 2>/dev/null; then
  echo "ОШИБКА: прогрев не завершился — не у всех сервисов есть конечный rate." >&2
  echo "        На холодном стенде Java экспортирует гистограмму только после" >&2
  echo "        первых запросов, а rate требует двух точек скрейпа. Измерять" >&2
  echo "        сейчас значит получить NaN и ложный отказ." >&2
  exit 1
fi

# --- 0.7. хвост прогрева должен доехать --------------------------------------
#
# Прогрев выше гоняет 120 запросов. Их метрики доезжают не мгновенно, и если
# снять базу сразу, они попадут в дельту ИЗМЕРЯЕМОГО прогона — как лишние.
#
# Дефект был здесь с самого начала, но маскировался: на холодном стенде цикл
# прогрева сам ждал появления конечного rate, и за это время всё успевало
# доехать. На тёплом стенде условие выполняется сразу («ждали 0с»), паузы не
# остаётся, и проверка выдаёт «счётчик заказов 251 (ожидалось 186) — ЛИШНЕЕ»
# на полностью исправном стенде.
#
# Ждём именно ТИШИНЫ: два одинаковых замера с интервалом 15 с. Интервал взят
# заведомо больше scrape_interval (5 с) — иначе «значение не изменилось» означало
# бы всего лишь «нового скрейпа не было», и ожидание завершилось бы раньше
# доставки. Тот же критерий с интервалом 5 с в другой проверке этого стенда уже
# давал ложный результат.
log "жду, пока метрики прогрева доедут (иначе попадут в дельту прогона)"
settle_prev=""
settle_i=0
while [ "$settle_i" -lt 10 ]; do
  settle_cur="$(prom_sum 'sum(khorost_tech_orders_created_total)')"
  if [ "$settle_cur" = "$settle_prev" ]; then
    break
  fi
  settle_prev="$settle_cur"
  settle_i=$((settle_i + 1))
  sleep 15
done
echo "  счётчик успокоился на $settle_prev (ждали $((settle_i * 15))с)"

# --- 1. счёт до прогона ------------------------------------------------------

log "снимаю значения ДО прогона"
BEFORE_ORDERS="$(prom_sum 'sum(khorost_tech_orders_created_total)')"
BEFORE_HIST="$(prom_sum 'sum(khorost_tech_order_duration_seconds_count)')"
[ "$BEFORE_ORDERS" = "EMPTY" ] && BEFORE_ORDERS=0
[ "$BEFORE_HIST" = "EMPTY" ] && BEFORE_HIST=0
echo "  счётчик заказов: $BEFORE_ORDERS, наблюдений гистограммы: $BEFORE_HIST"

# --- 2. прогон ---------------------------------------------------------------

RUN_ID="run-$(date +%s)"
log "прогон нагрузки: $REQUESTS запросов, seed $SEED"
LOAD_OUT="$STAND_DIR/results/.last-load-metrics.json"
rm -f "$LOAD_OUT"
# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED" \
  -concurrency 4 -run "$RUN_ID" -json 2>/dev/null | sed -n '/^{/,$p' > "$LOAD_OUT" || true

if [ ! -s "$LOAD_OUT" ]; then
  echo "ОШИБКА: генератор не записал итог, сверять нечего" >&2
  exit 1
fi

EXPECTED_OK="$("$PYTHON_BIN" -c "
import sys, json
print(json.load(sys.stdin)['expected_by_status'].get('201', 0))
" < "$LOAD_OUT")"
ACTUAL_OK="$("$PYTHON_BIN" -c "
import sys, json
print(json.load(sys.stdin)['actual_by_status'].get('201', 0))
" < "$LOAD_OUT")"
EXPECTED_ERR="$("$PYTHON_BIN" -c "
import sys, json
a = json.load(sys.stdin)['expected_by_status']
print(a.get('404', 0) + a.get('502', 0))
" < "$LOAD_OUT")"
EXPECTED_TOTAL=$((EXPECTED_OK + EXPECTED_ERR))
echo "  план: 201 = $EXPECTED_OK, прочих = $EXPECTED_ERR; факт 201 = $ACTUAL_OK"

if [ "$EXPECTED_OK" != "$ACTUAL_OK" ]; then
  echo "ОШИБКА: сам прогон разошёлся с планом — метрики сверять бессмысленно" >&2
  exit 1
fi

# --- 3. счётчик и гистограмма ------------------------------------------------

log "жду доставки метрик (batch-экспорт + скрейп)"
sleep 20
AFTER_ORDERS="$(prom_sum 'sum(khorost_tech_orders_created_total)')"
AFTER_HIST="$(prom_sum 'sum(khorost_tech_order_duration_seconds_count)')"
DELTA_ORDERS="$("$PYTHON_BIN" -c "print(int(float('$AFTER_ORDERS') - float('$BEFORE_ORDERS')))")"
DELTA_HIST="$("$PYTHON_BIN" -c "print(int(float('$AFTER_HIST') - float('$BEFORE_HIST')))")"
echo "  счётчик: дельта $DELTA_ORDERS (ожидалось $EXPECTED_OK)"
echo "  гистограмма: дельта наблюдений $DELTA_HIST (ожидалось $EXPECTED_TOTAL)"

# --- 4. цена гистограммы в сериях --------------------------------------------

# Главное число статьи 4: во сколько серий обходится одна гистограмма против
# одного счётчика. Считается по факту, а не по формуле «бакетов + 2»: в реальном
# экспорте бывают сюрпризы вроде отсутствующего +Inf или добавленных лейблов.
log "считаю фактическую цену гистограммы в сериях"
N_COUNTER="$(prom_series 'khorost_tech_orders_created_total')"
N_BUCKET="$(prom_series 'khorost_tech_order_duration_seconds_bucket')"
N_SUM="$(prom_series 'khorost_tech_order_duration_seconds_sum')"
N_COUNT="$(prom_series 'khorost_tech_order_duration_seconds_count')"
N_HIST_TOTAL=$((N_BUCKET + N_SUM + N_COUNT))
# Серий на одну комбинацию лейблов: делим на число комбинаций, а его даёт _count
# (по одной серии на комбинацию).
if [ "$N_COUNT" -gt 0 ] 2>/dev/null; then
  PER_COMBO=$((N_HIST_TOTAL / N_COUNT))
else
  PER_COMBO=0
fi
echo "  счётчик: $N_COUNTER серий"
echo "  гистограмма: $N_BUCKET бакетов + $N_SUM sum + $N_COUNT count = $N_HIST_TOTAL"
echo "  на одну комбинацию лейблов: $PER_COMBO серий"

# --- 5. правила RED дают значения --------------------------------------------

log "правила RED: проверяю, что каждое возвращает значение, а не пустоту"
RED_RATE="$(wait_nonempty 'job:http_server_requests:rate5m')" || true
RED_ERR="$(prom_by_service 'job:http_server_requests_errors:rate5m')"
RED_RATIO="$(prom_by_service 'job:http_server_requests_errors:ratio5m')"
RED_P95="$(prom_by_service 'job:http_server_request_duration_seconds:p95_5m')"
# Отдельно и одним числом — чтобы вердикт мог поймать NaN. В разрезе по сервисам
# NaN потерялся бы в общей строке.
RED_P95_GO="$(prom_sum 'job:http_server_request_duration_seconds:p95_5m{service_name="go-frontend"}')"
ORDER_P95="$(prom_sum 'job:order_duration_seconds:p95_5m')"
echo "  R (сумма по сервисам): $RED_RATE"
echo "  E (rate):   $RED_ERR"
echo "  E (доля):   $RED_RATIO"
echo "  D (p95):    $RED_P95"
echo "  p95 бизнес-операции: $ORDER_P95"

# Сколько сервисов отдают серверные HTTP-метрики. Правила RED обязаны покрывать
# ровно столько же: если сервис есть в исходной метрике, но пропал в правиле —
# это и есть замалчивание, ради поимки которого проверка написана.
SERVICES_RAW="$(prom_series 'count by (service_name) (http_server_request_duration_seconds_count)')"
SERVICES_RATE="$(prom_series 'job:http_server_requests:rate5m')"
SERVICES_ERR="$(prom_series 'job:http_server_requests_errors:rate5m')"
SERVICES_RATIO="$(prom_series 'job:http_server_requests_errors:ratio5m')"
SERVICES_P95="$(prom_series 'job:http_server_request_duration_seconds:p95_5m')"
echo "  сервисов в исходной метрике: $SERVICES_RAW"
echo "  сервисов в правилах: rate=$SERVICES_RATE errors=$SERVICES_ERR ratio=$SERVICES_RATIO p95=$SERVICES_P95"

# Покрытие сервисов — только половина дела. Вторая половина: у КАЖДОГО значение
# должно быть конечным. Считаем отдельно и печатаем виновников.
log "проверяю КОНЕЧНОСТЬ значений RED у каждого сервиса"
read -r FIN_RATE  NAN_RATE  <<<"$(prom_finite 'job:http_server_requests:rate5m')"
read -r FIN_ERR   NAN_ERR   <<<"$(prom_finite 'job:http_server_requests_errors:rate5m')"
read -r FIN_RATIO NAN_RATIO <<<"$(prom_finite 'job:http_server_requests_errors:ratio5m')"
read -r FIN_P95   NAN_P95   <<<"$(prom_finite 'job:http_server_request_duration_seconds:p95_5m')"
echo "  конечных значений: rate=$FIN_RATE errors=$FIN_ERR ratio=$FIN_RATIO p95=$FIN_P95 (сервисов в метрике: $SERVICES_RAW)"
for pair in "rate:$NAN_RATE" "errors:$NAN_ERR" "ratio:$NAN_RATIO" "p95:$NAN_P95"; do
  name="${pair%%:*}"; bad="${pair#*:}"
  [ "$bad" != "-" ] && echo "    НЕ КОНЕЧНО в $name: $bad"
done

# --- 6. правила USE ----------------------------------------------------------

log "правила USE: пул соединений и очередь экспортёра"
USE_UTIL="$(prom_by_service 'job:db_pool:utilization')"
USE_PEND="$(prom_by_service 'job:db_pool:pending')"
QUEUE_UTIL="$(prom_sum 'job:otelcol_exporter_queue:utilization')"
echo "  U пула: $USE_UTIL"
echo "  S пула (ожидающие): $USE_PEND"
echo "  очередь экспортёра: $QUEUE_UTIL"

# --- вердикт -----------------------------------------------------------------

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-34s %s (ожидалось ровно %s)\n" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-34s %s (ожидалось ровно %s)" "$name" "$got" "$want"
    if "$PYTHON_BIN" -c "import sys; sys.exit(0 if float('$got') > float('$want') else 1)" 2>/dev/null; then
      printf " — ЛИШНЕЕ\n"
    else
      printf " — НЕДОСТАЧА\n"
    fi
    FAILED=1
  fi
}

# Отдельный вердикт для «значение есть»: критерий не число, а само наличие.
#
# NaN проверяется наравне с пустотой, и это не педантизм. histogram_quantile от
# нулевого rate возвращает именно NaN, а не пустоту — то есть серия есть, правило
# health="ok", на дашборде разрыв линии. Хуже другое: NaN не проходит НИ ОДНО
# сравнение. Проверено на стенде — при остановленном трафике и p95=NaN не
# срабатывает ни `p95 > 0.5` (алерт на деградацию), ни `p95 < 0.5` (обратная
# проверка живости). Алерт по латентности молчит в обе стороны, и это молчание
# выглядит точно как здоровый сервис.
verdict_present() {
  local name="$1" got="$2"
  case "$got" in
    EMPTY | ERR | -1)
      printf "  ОТКАЗ   %-34s правило вычисляется, но значения нет\n" "$name"
      FAILED=1
      ;;
    nan | NaN | NAN)
      printf "  ОТКАЗ   %-34s NaN — трафика в окне нет, алерт поверх молчал бы\n" "$name"
      FAILED=1
      ;;
    *)
      printf "  ГОДЕН   %-34s %s\n" "$name" "$got"
      ;;
  esac
}

echo
log "вердикт"
verdict "счётчик заказов (дельта)"        "$DELTA_ORDERS" "$EXPECTED_OK"
verdict "наблюдений гистограммы (дельта)" "$DELTA_HIST"   "$EXPECTED_TOTAL"
verdict "правил с ошибкой вычисления"     "$RULES_BAD"    "0"
verdict_present "R: rate запросов"        "$RED_RATE"
verdict_present "D: p95 HTTP go-frontend" "$RED_P95_GO"
verdict_present "D: p95 бизнес-операции"  "$ORDER_P95"
verdict "сервисов в правиле rate"         "$SERVICES_RATE"  "$SERVICES_RAW"
verdict "сервисов в правиле errors"       "$SERVICES_ERR"   "$SERVICES_RAW"
verdict "сервисов в правиле ratio"        "$SERVICES_RATIO" "$SERVICES_RAW"
verdict "сервисов в правиле p95"          "$SERVICES_P95"   "$SERVICES_RAW"
# Конечность значений — отдельные критерии. Без них проверка проходила при NaN у
# одного из сервисов: покрытие совпадало, а значение было непригодным.
verdict "конечных значений R (rate)"      "$FIN_RATE"    "$SERVICES_RAW"
verdict "конечных значений E (rate)"      "$FIN_ERR"     "$SERVICES_RAW"
verdict "конечных значений E (доля)"      "$FIN_RATIO"   "$SERVICES_RAW"
verdict "конечных значений D (p95)"       "$FIN_P95"     "$SERVICES_RAW"

echo
if [ "$FAILED" = 0 ]; then
  echo "ИТОГ: метрики RED/USE считаются и сходятся с прогоном."
else
  echo "ИТОГ: есть расхождения — смотри строки ОТКАЗ выше."
fi

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Самопроверка метрик RED/USE"
  echo "==========================="
  echo "Прогон: $REQUESTS запросов, seed $SEED, метка $RUN_ID"
  echo
  echo "План нагрузки: 201 = $EXPECTED_OK, прочих = $EXPECTED_ERR, всего = $EXPECTED_TOTAL"
  echo
  echo "Счётчик и гистограмма"
  echo "  khorost_tech_orders_created_total   дельта $DELTA_ORDERS (ожидалось $EXPECTED_OK)"
  echo "  khorost_tech_order_duration_seconds дельта $DELTA_HIST (ожидалось $EXPECTED_TOTAL)"
  echo
  echo "Цена в сериях"
  echo "  счётчик:      $N_COUNTER"
  echo "  гистограмма:  $N_BUCKET бакетов + $N_SUM sum + $N_COUNT count = $N_HIST_TOTAL"
  echo "  на комбинацию лейблов: $PER_COMBO серий против 1 у счётчика"
  echo
  echo "Правила RED"
  echo "  R сумма:  $RED_RATE"
  echo "  E rate:   $RED_ERR"
  echo "  E доля:   $RED_RATIO"
  echo "  D p95:    $RED_P95"
  echo "  p95 бизнес-операции: $ORDER_P95"
  echo "  сервисов: исходно $SERVICES_RAW, в правилах rate=$SERVICES_RATE errors=$SERVICES_ERR ratio=$SERVICES_RATIO p95=$SERVICES_P95"
  echo "  КОНЕЧНЫХ значений: rate=$FIN_RATE errors=$FIN_ERR ratio=$FIN_RATIO p95=$FIN_P95"
  echo "    (покрытие сервисов и конечность значений — разные критерии: серия может"
  echo "     существовать со значением NaN, и тогда алерт поверх правила молчит)"
  for pair in "rate:$NAN_RATE" "errors:$NAN_ERR" "ratio:$NAN_RATIO" "p95:$NAN_P95"; do
    name="${pair%%:*}"; bad="${pair#*:}"
    [ "$bad" != "-" ] && echo "    НЕ КОНЕЧНО в $name: $bad"
  done
  echo
  echo "Правила USE"
  echo "  U пула: $USE_UTIL"
  echo "  S пула: $USE_PEND"
  echo "  очередь экспортёра: $QUEUE_UTIL"
  echo
  echo "Правил загружено: $RULES_TOTAL, с ошибкой вычисления: $RULES_BAD"
  if [ "$FAILED" = 0 ]; then
    echo "ИТОГ: ГОДЕН"
  else
    echo "ИТОГ: ОТКАЗ"
  fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
