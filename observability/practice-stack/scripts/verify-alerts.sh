#!/usr/bin/env bash
# Самопроверка SLO-алертов: срабатывает ли алерт на деградации, доезжает ли
# уведомление до приёмника — и, что не менее важно, НЕ горит ли он на нормальной
# нагрузке.
#
#   ./scripts/verify-alerts.sh [запросов_с_ошибками] [запросов_нормальных]
#
# Вторая половина проверки не формальность. Алерт, который горит всегда, тоже
# «сработал»: проверка только на срабатывание пропускает и слишком низкий порог,
# и правило, которое вычисляет мусор. Поэтому сценарий из двух фаз, и обе
# обязательны.
#
# Заодно замеряется фактическая задержка от начала деградации до firing — это
# реальная цена окон в правилах, и она интереснее теории.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

BAD_REQUESTS="${1:-400}"
GOOD_REQUESTS="${2:-300}"
RESULT_FILE="$STAND_DIR/results/08-alerts.txt"
ALERT_NAME="SLOBurnRateFast"

require_tools

# --- аварийное восстановление стенда ------------------------------------------
#
# Фаза 4 останавливает java-backend по-настоящему и держит его остановленным
# несколько минут. Всё это время проверку могут прервать: Ctrl+C на долгом
# ожидании, kill, ошибка в любой команде между остановкой и подъёмом (скрипт
# работает с `set -e`). Без обработчика сервис так и останется лежать, а стенд —
# в ложном аварийном состоянии: следующий прогон любой проверки увидит мёртвый
# java-backend и объявит отказ, причём причина будет выглядеть как дефект стенда,
# а не как след прерванной проверки.
#
# Поэтому подъём делается не только на нормальном пути, но и из trap. Флаг
# снимается сразу после успешного подъёма, так что обработчик идемпотентен:
# EXIT-trap, срабатывающий после INT- или TERM-обработчика, ничего не повторит.
#
# ДВЕ ОГОВОРКИ, обе проверены живьём остановкой java-backend и сигналами.
#
# 1. Bash выполняет trap не в момент сигнала, а ПОСЛЕ завершения текущей команды
#    переднего плана. С одним длинным `sleep 600` восстановление ждало бы все 600
#    секунд — то есть формально работало бы, а практически нет. Поэтому все
#    ожидания фазы 4 сделаны циклами с коротким `sleep 10`: задержка подъёма не
#    превышает десяти секунд. Заменять эти циклы одним длинным ожиданием нельзя.
#
# 2. Если запустить проверку фоном (`./scripts/verify-alerts.sh &`), оболочка
#    выставит SIGINT в игнорирование ещё до старта скрипта, а сигнал, уже
#    игнорируемый на входе, перехватить невозможно — `trap ... INT` в таком
#    запуске просто не установится. SIGTERM это не затрагивает, и EXIT-путь тоже
#    работает. Для фонового запуска пользуйтесь `kill -TERM`, а не Ctrl+C.
#
# Что именно проверено: (а) ошибка в середине фазы под `set -e` — сервис поднят,
# код возврата 1 сохранён; (б) SIGTERM в цикле ожидания — поднят; (в) SIGINT на
# переднем плане — поднят, код возврата 130.
JAVA_STOPPED=0

restore_java_backend() {
  local rc=$?
  if [ "$JAVA_STOPPED" = "1" ]; then
    echo "" >&2
    echo "ВОССТАНОВЛЕНИЕ: java-backend был остановлен фазой 4 — поднимаю обратно." >&2
    docker compose start java-backend >/dev/null 2>&1 || {
      echo "ВНИМАНИЕ: поднять java-backend не удалось. Стенд остался в аварийном" >&2
      echo "          состоянии, следующие прогоны будут врать. Подними вручную:" >&2
      echo "          docker compose start java-backend" >&2
    }
    JAVA_STOPPED=0
  fi
  return $rc
}

trap restore_java_backend EXIT
# Отдельные обработчики для сигналов: с одним только EXIT прерывание по Ctrl+C
# отрабатывает, но код возврата теряет причину. 130 и 143 — привычные коды для
# SIGINT и SIGTERM, по ним видно, что прогон прервали, а не что он провалился.
trap 'restore_java_backend; exit 130' INT
trap 'restore_java_backend; exit 143' TERM

# --- запросы к Prometheus и Alertmanager -------------------------------------

# Состояние алерта по API правил: inactive | pending | firing.
alert_state() {
  local name="$1"
  innet -s --max-time 15 "http://prometheus:9090/api/v1/rules?type=alert" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    gs = json.load(sys.stdin)['data']['groups']
    for g in gs:
        for r in g['rules']:
            if r.get('name') == '$name':
                print(r.get('state', 'unknown'))
                sys.exit(0)
    print('absent')
except Exception:
    print('ERR')
"
}

# Сколько алертов с этим именем видит сам Alertmanager. Отдельно от состояния в
# Prometheus: алерт может гореть в Prometheus и не доехать до Alertmanager, и это
# разные отказы с разными причинами.
am_alert_count() {
  local name="$1"
  innet -s --max-time 15 "http://alertmanager:9093/api/v2/alerts" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(sum(1 for a in d if a.get('labels', {}).get('alertname') == '$name'))
except Exception:
    print(-1)
"
}

# Что получил приёмник. Считаются уведомления и алерты по статусу отдельно:
# firing и resolved приходят одним и тем же путём, и путать их нельзя.
webhook_received() {
  innet -s --max-time 15 "http://alert-webhook:9099/alerts" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    print(json.load(sys.stdin)['received'])
except Exception:
    print(-1)
"
}

webhook_count() {
  local name="$1" status="$2"
  innet -s --max-time 15 "http://alert-webhook:9099/alerts" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(sum(1 for a in (d.get('alerts') or [])
              if a.get('labels', {}).get('alertname') == '$name'
              and a.get('status') == '$status'))
except Exception:
    print(-1)
"
}

webhook_reset() {
  innet -s --max-time 15 -X POST "http://alert-webhook:9099/reset" >/dev/null 2>&1 || true
}

# Одно скалярное значение запроса; пустой результат — ноль. Отдельная функция,
# потому что ожидание опустошения окон сравнивает ДВА значения, и считать максимум
# надо снаружи Prometheus.
prom_scalar() {
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query"     --data-urlencode "query=$1" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    rs = json.load(sys.stdin)['data']['result']
    print('%.6f' % float(rs[0]['value'][1]) if rs else '0')
except Exception:
    print('0')
"
}

# Недоставленные уведомления. Метрика самого Alertmanager: приёмник, который не
# отвечает HTTP-ответом, считается недоступным (шесть попыток и явный отказ), и
# видно это только здесь либо в логах.
am_failed_notifications() {
  innet -s --max-time 15 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=sum(alertmanager_notifications_failed_total)" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    rs = json.load(sys.stdin)['data']['result']
    print(int(float(rs[0]['value'][1])) if rs else 0)
except Exception:
    print(-1)
"
}

# Ждать состояния алерта, возвращая затраченное время. Именно время и есть
# предмет замера, поэтому цикл не прячет его внутри.
wait_state() {
  local name="$1" want="$2" limit="${3:-180}" started elapsed state
  started="$(date +%s)"
  while :; do
    state="$(alert_state "$name")"
    elapsed=$(( $(date +%s) - started ))
    if [ "$state" = "$want" ]; then
      echo "$elapsed"
      return 0
    fi
    if [ "$elapsed" -ge "$limit" ]; then
      echo "$elapsed"
      return 1
    fi
    sleep 3
  done
}

# --- предварительные условия -------------------------------------------------

log "проверяю, что алерты вообще загружены"
STATE_BEFORE="$(alert_state "$ALERT_NAME")"
echo "  состояние $ALERT_NAME до сценария: $STATE_BEFORE"
if [ "$STATE_BEFORE" = "absent" ] || [ "$STATE_BEFORE" = "ERR" ]; then
  echo "ОШИБКА: Prometheus не знает правила $ALERT_NAME. Проверь rule_files и reload." >&2
  exit 1
fi

# Алерт мог остаться горящим с прошлого прогона. Сценарий начинается с покоя:
# иначе «сработал» ничего не докажет — он уже горел.
if [ "$STATE_BEFORE" != "inactive" ]; then
  log "алерт не в покое, жду затухания (окна rate должны опустеть)"
  QUIET="$(wait_state "$ALERT_NAME" inactive 300)" || {
    echo "ОШИБКА: алерт не погас за ${QUIET}с. Прошлая нагрузка ещё в окне rate;" >&2
    echo "        начинать сценарий сейчас — значит проверять неизвестно что." >&2
    exit 1
  }
  echo "  погас за ${QUIET}с"
fi

# --- фаза 0.5: система здорова ------------------------------------------------
#
# Проверка «алерт НЕ горит на нормальной нагрузке» имеет смысл только если стенд
# исправен до начала сценария. Свежеподнятый или только что перезапущенный
# java-backend несколько десятков секунд отвечает ошибками, go-frontend отдаёт на
# них 502 — и дальше вся проверка меряет не алерт, а неготовый стенд.
#
# ВНИМАНИЕ на критерий: пробная нагрузка — ОТДЕЛЬНЫЙ прогон без намеренных
# отказов, поэтому здесь уместно точное «все успешны». Основной сценарий
# «нормальной» нагрузки устроен иначе: он НАМЕРЕННО содержит 16 ответов 404 и 13
# ответов 502 на 300 запросов, и 271 успешный ответ там — ожидаемое число, а не
# признак поломки. Я один раз принял это число за симптом и ошибся: в отчёте
# loadgen стоял mismatch: 0, то есть фактическое совпало с ожидаемым полностью.
log "проверяю, что система здорова (иначе «нормальная нагрузка» не нормальная)"
HEALTH_LIMIT="${HEALTH_LIMIT:-20}"
health_i=0
HEALTH_OK=0
HEALTH_PROBE=20
while [ "$health_i" -lt "$HEALTH_LIMIT" ]; do
  docker compose run --rm --no-deps -T loadgen     -target http://go-frontend:8080 -requests "$HEALTH_PROBE" -concurrency 2     -run "alerts-health" -json 2>/dev/null | sed -n '/^{/,$p' > "$STAND_DIR/results/.last-load-health.json" || true
  # Успех считается по ВСЕМ кодам 2xx, а не по «200»: создание заказа отвечает
  # 201, и первая версия критерия искала ровно «200», не находила ни одного и
  # ждала здоровья от исправного стенда до самого предела. Заодно требуем нулевых
  # транспортных ошибок — запрос, не дошедший до сервиса, в разбивку по кодам не
  # попадает вовсе и молча не учитывается.
  HEALTH_OK="$("$PYTHON_BIN" -c "
import sys, json
try:
    d = json.load(sys.stdin)
    if d.get('transport_errors', 0):
        print(0)
    else:
        print(sum(v for k, v in d['actual_by_status'].items() if k.startswith('2')))
except Exception:
    print(0)
" < "$STAND_DIR/results/.last-load-health.json")"
  [ "$HEALTH_OK" = "$HEALTH_PROBE" ] && break
  health_i=$((health_i + 1))
  sleep 10
done
echo "  успешных из $HEALTH_PROBE пробных: $HEALTH_OK (ждали $((health_i * 10))с)"

if [ "$HEALTH_OK" != "$HEALTH_PROBE" ]; then
  echo "ОШИБКА: система не отвечает нормально на исправной нагрузке." >&2
  echo "        Начинать сценарий сейчас — значит принять неисправность стенда" >&2
  echo "        за ложное срабатывание алерта. Проверь java-backend." >&2
  exit 1
fi

# Дождаться, пока окно rate ОПУСТЕЕТ, а не только пока алерт погас. Разница
# принципиальная, и она стоила одного прогона.
#
# Burn rate нормирован на объём: это его достоинство, но и условие проверки.
# После 4000 успешных запросов (прогон verify-profiles) фаза деградации из 400
# ответов 502 дала долю ошибок 400/4400 = 9% — ниже порога 14.4%, и алерт
# СПРАВЕДЛИВО не сработал. Проверка при этом отрапортовала ОТКАЗ, хотя правило
# отработало ровно так, как задумано.
#
# Отсюда и требование: перед фазой деградации в окне не должно оставаться
# постороннего трафика.
# Ждать опустошения ОБОИХ окон, а не только короткого. Условие быстрого алерта —
# конъюнкция по 5m И 10m; длинное окно опустошается позже, и пока в нём остаётся
# прежний успешный трафик, доля ошибок не дотягивает до порога 14.4.
#
# Поймано на прогоне: короткое окно опустело, проверка начала фазу деградации, и
# зажёгся только SLOBurnRateSlow (порог 6), а Fast (порог 14.4) остался inactive —
# ровно потому, что 10m ещё помнило успешные запросы предыдущего сценария.
# Ожидание по одному окну давало ложный ОТКАЗ на исправном правиле.
log "жду, пока опустеют ОБА окна rate (5m и 10m), иначе деградация размоется"
i=0
RATE_NOW="?"
while [ "$i" -lt 60 ]; do
  # Максимум берётся в ШЕЛЛЕ, двумя отдельными запросами. В PromQL нет функции
  # «максимум из двух выражений»: `max(A or B)` вернуло бы A, если A существует
  # (оператор `or` не объединяет значения, а выбирает левую сторону), и длинное
  # окно так и осталось бы непроверенным.
  rate_short="$(prom_scalar 'sum(job:http_server_requests:rate5m)')"
  rate_long="$(prom_scalar 'sum(rate(http_server_request_duration_seconds_count[10m]))')"
  RATE_NOW="$("$PYTHON_BIN" -c "
import sys
try:
    print('%.4f' % max(float('$rate_short'), float('$rate_long')))
except Exception:
    print('ERR')
")"
  # Порог 0.05 запроса в секунду: полный ноль недостижим, healthcheck и скрейпы
  # дают фон. 0.05 — это меньше одного запроса за 20 секунд.
  if "$PYTHON_BIN" -c "import sys; sys.exit(0 if float('$RATE_NOW') < 0.05 else 1)" 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 15
done
echo "  rate запросов в окне: $RATE_NOW"

# Если окно так и не опустело — ОСТАНОВИТЬСЯ, а не продолжать. Раньше цикл просто
# исчерпывал лимит, печатал остаточный rate и шёл дальше: фаза деградации
# начиналась при непустом окне, доля ошибок размывалась прежним успешным
# трафиком, алерт справедливо не зажигался, и проверка объявляла ОТКАЗ на
# исправном правиле. Молчаливое продолжение здесь хуже честного отказа: оно
# выдаёт условия проверки за проверяемое поведение.
if "$PYTHON_BIN" -c "import sys; sys.exit(0 if float('$RATE_NOW') >= 0.05 else 1)" 2>/dev/null; then
  echo "ОШИБКА: окна rate не опустели (осталось $RATE_NOW запросов/с)." >&2
  echo "        Деградация размоется прежним трафиком, и алерт не зажжётся —" >&2
  echo "        проверять сейчас бессмысленно. Подождите ещё несколько минут" >&2
  echo "        (длинное окно 10m опустошается примерно за свою длину после" >&2
  echo "        последней нагрузки) и запустите проверку заново." >&2
  exit 1
fi

FAILED_BEFORE="$(am_failed_notifications)"
webhook_reset
echo "  приёмник обнулён, недоставленных уведомлений до сценария: $FAILED_BEFORE"

# --- фаза 1: деградация ------------------------------------------------------

log "ФАЗА 1: нагрузка из одних отказов сервера ($BAD_REQUESTS запросов)"
DEGRADATION_START="$(date +%s)"
# ВНИМАНИЕ на --no-deps. Без него `docker compose run` поднимает всю цепочку
# depends_on, и сервис, намеренно остановленный для опыта, молча возвращается к
# жизни: loadgen зависит от go-frontend, тот от java-backend. Проверено —
# остановленный java-backend поднимался обратно, его rate продолжал расти, и
# опыт «сервис замолчал» не воспроизводился вовсе. Стенд к этому моменту уже
# поднят up.sh, так что зависимости здесь не нужны.
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$BAD_REQUESTS" -errors-only \
  -concurrency 4 -run "alerts-bad" -json 2>/dev/null | sed -n '/^{/,$p' > "$STAND_DIR/results/.last-load-alerts.json" || true

BAD_502="$("$PYTHON_BIN" -c "
import sys, json
print(json.load(sys.stdin)['actual_by_status'].get('502', 0))
" < "$STAND_DIR/results/.last-load-alerts.json")"
echo "  фактически получено 502: $BAD_502 из $BAD_REQUESTS"

if [ "$BAD_502" != "$BAD_REQUESTS" ]; then
  echo "ОШИБКА: деградация не воспроизвелась ($BAD_502 из $BAD_REQUESTS)." >&2
  echo "        Проверять алерт бессмысленно: нечему срабатывать." >&2
  exit 1
fi

log "жду перехода $ALERT_NAME в firing"
rc_fire=0
TIME_TO_FIRING="$(wait_state "$ALERT_NAME" firing 240)" || rc_fire=$?
# Задержка считается от НАЧАЛА нагрузки, а не от её конца: интересна цена окон,
# а не длительность прогона.
TOTAL_TO_FIRING=$(( $(date +%s) - DEGRADATION_START ))
echo "  ожидание firing: ${TIME_TO_FIRING}с (от начала деградации: ${TOTAL_TO_FIRING}с)"

STATE_FIRING="$(alert_state "$ALERT_NAME")"
AM_COUNT="$(am_alert_count "$ALERT_NAME")"
echo "  состояние в Prometheus: $STATE_FIRING"
echo "  алертов с этим именем в Alertmanager: $AM_COUNT"

log "жду доставки уведомления приёмнику"
i=0
WEBHOOK_FIRING=0
while [ "$i" -lt 30 ]; do
  WEBHOOK_FIRING="$(webhook_count "$ALERT_NAME" firing)"
  [ "$WEBHOOK_FIRING" -gt 0 ] 2>/dev/null && break
  i=$((i + 1))
  sleep 2
done
WEBHOOK_TOTAL="$(webhook_received)"
echo "  уведомлений получено приёмником: $WEBHOOK_TOTAL"
echo "  из них алертов $ALERT_NAME в состоянии firing: $WEBHOOK_FIRING"

FAILED_AFTER="$(am_failed_notifications)"
FAILED_DELTA=$((FAILED_AFTER - FAILED_BEFORE))
echo "  недоставленных уведомлений за сценарий: $FAILED_DELTA"

# --- фаза 2: покой -----------------------------------------------------------
#
# Обязательная половина. Проверяем, что алерт гаснет, а на нормальной нагрузке
# не зажигается снова.

log "ФАЗА 2: жду затухания алерта"
rc_quiet=0
TIME_TO_QUIET="$(wait_state "$ALERT_NAME" inactive 300)" || rc_quiet=$?
echo "  затухание: ${TIME_TO_QUIET}с"

# Уведомление resolved приходит ПОЗЖЕ, чем алерт становится inactive: Prometheus
# сначала снимает алерт, потом Alertmanager выдерживает group_interval и только
# затем отправляет. Считать сразу после затухания — значит проверять до события.
#
# Поймано на финальной сверке волны 3: вердикт объявил «уведомлений resolved 0»,
# а в приёмнике оно к тому моменту уже лежало — просто пришло на несколько
# секунд позже. Ложный отказ на исправной доставке, причём именно там, где
# проверка должна была подтверждать доставку.
log "жду уведомления resolved (оно приходит позже, чем алерт гаснет)"
res_i=0
WEBHOOK_RESOLVED=0
while [ "$res_i" -lt 20 ]; do
  WEBHOOK_RESOLVED="$(webhook_count "$ALERT_NAME" resolved)"
  [ "$WEBHOOK_RESOLVED" -ge 1 ] 2>/dev/null && break
  res_i=$((res_i + 1))
  sleep 5
done
echo "  уведомлений resolved: $WEBHOOK_RESOLVED (ждали $((res_i * 5))с)"

# Погасший алерт ЕЩЁ НЕ ЗНАЧИТ, что окно очистилось. Алерт становится inactive,
# как только доля ошибок опускается ниже порога, а ошибки фазы деградации при этом
# остаются в окне 10m ещё несколько минут. Нормальная нагрузка добавляет к ним
# свои собственные отказы (сценарий намеренно содержит 13 ответов 502 на 300
# запросов — это правдоподобный трафик, а не идеальный), сумма снова переваливает
# порог, и проверка объявляет ложное срабатывание на исправном алерте.
#
# Поймано прогоном, и сначала объяснено неверно: в выводе стояло «успешных 271 из
# 300», я счёл это признаком неисправного стенда. Разбивка ответов показала
# mismatch: 0 — то есть 271 успешный ответ был ОЖИДАЕМЫМ числом. Число было
# верным, неверным было приписанное ему значение.
#
# Что именно зажгло алерт в том прогоне — НЕ УСТАНОВЛЕНО, воспроизвести не
# удалось. Это ожидание — страховка от описанного механизма, а не подтверждённое
# лекарство: на последующих прогонах оно отрабатывало за 0 секунд.
log "жду, пока ошибки деградации покинут окно (inactive — ещё не пусто)"
eq_i=0
ERR_RATE="?"
while [ "$eq_i" -lt 40 ]; do
  ERR_RATE="$(prom_scalar 'sum(job:http_server_requests_errors:rate5m)')"
  if "$PYTHON_BIN" -c "import sys; sys.exit(0 if float('$ERR_RATE') < 0.02 else 1)" 2>/dev/null; then
    break
  fi
  eq_i=$((eq_i + 1))
  sleep 15
done
echo "  rate ошибок в окне: $ERR_RATE (ждали $((eq_i * 15))с)"

webhook_reset
log "ФАЗА 2: нормальная нагрузка ($GOOD_REQUESTS запросов), алерт гореть НЕ должен"
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$GOOD_REQUESTS" \
  -concurrency 4 -run "alerts-good" -json 2>/dev/null | sed -n '/^{/,$p' > "$STAND_DIR/results/.last-load-alerts-good.json" || true

GOOD_OK="$("$PYTHON_BIN" -c "
import sys, json
print(json.load(sys.stdin)['actual_by_status'].get('201', 0))
" < "$STAND_DIR/results/.last-load-alerts-good.json")"
echo "  успешных в нормальном прогоне: $GOOD_OK"

# Ждём с запасом: окно 5m плюс for. Если алерт зажжётся — увидим.
log "наблюдаю 90с, что алерт не зажёгся"
WORST_STATE="inactive"
for i in $(seq 1 30); do
  st="$(alert_state "$ALERT_NAME")"
  case "$st" in
    firing) WORST_STATE="firing"; break ;;
    pending) WORST_STATE="pending" ;;
  esac
  sleep 3
done
echo "  худшее наблюдённое состояние: $WORST_STATE"
FALSE_FIRING="$(webhook_count "$ALERT_NAME" firing)"
echo "  ложных уведомлений firing на нормальной нагрузке: $FALSE_FIRING"

# --- фаза 3: подавление производных алертов -----------------------------------
#
# Проверяется отдельно и синтетическими алертами через API, а не сценарием
# нагрузки. Причина: чтобы естественным путём столкнуть ServiceDown и SLO-алерт
# по одному сервису, нужен сервис, который одновременно мёртв и имеет ненулевой
# burn rate, — а это взаимоисключающие состояния. Через API оба алерта подаются
# напрямую, и проверяется ровно то, что должно работать: правило подавления.
#
# Реальный (не синтетический) сценарий ServiceDown проверяется отдельно, в фазе 4.
#
# Без этой фазы подавление остаётся непроверяемым, а именно там и была ошибка:
# источником стоял глобальный алерт без лейбла service_name, и `equal:
# [service_name]` не совпадал никогда.

log "ФАЗА 3: подавление производных алертов (синтетические алерты через API)"
PROBE_PAYLOAD='[
 {"labels":{"alertname":"ServiceDown","service_name":"probe-svc","severity":"critical","slo":"availability"},"annotations":{"summary":"источник подавления"}},
 {"labels":{"alertname":"SLOBurnRateFast","service_name":"probe-svc","severity":"critical","slo":"availability"},"annotations":{"summary":"цель: тот же сервис"}},
 {"labels":{"alertname":"SLOBurnRateFast","service_name":"probe-other","severity":"critical","slo":"availability"},"annotations":{"summary":"цель: другой сервис"}}
]'
innet -s --max-time 15 -X POST -H 'Content-Type: application/json' -d "$PROBE_PAYLOAD"   "http://alertmanager:9093/api/v2/alerts" >/dev/null 2>&1 || true
sleep 8

read -r INHIB_SAME INHIB_OTHER INHIB_SOURCE <<<"$(innet -s --max-time 15 "http://alertmanager:9093/api/v2/alerts" 2>/dev/null |
  "$PYTHON_BIN" -c "
import sys, json
same = other = source = 'нет'
try:
    for a in json.load(sys.stdin):
        l = a.get('labels', {})
        st = (a.get('status') or {}).get('state', '?')
        if l.get('alertname') == 'SLOBurnRateFast' and l.get('service_name') == 'probe-svc':
            same = st
        elif l.get('alertname') == 'SLOBurnRateFast' and l.get('service_name') == 'probe-other':
            other = st
        elif l.get('alertname') == 'ServiceDown' and l.get('service_name') == 'probe-svc':
            source = st
except Exception:
    pass
print('%s %s %s' % (same, other, source))
")"
echo "  цель того же сервиса:   $INHIB_SAME (ожидается suppressed)"
echo "  цель другого сервиса:   $INHIB_OTHER (ожидается active)"
echo "  сам источник:           $INHIB_SOURCE (ожидается active — сам себя не подавляет)"

# --- фаза 4: реальный ServiceDown ---------------------------------------------
#
# Фаза 3 доказывает только правило подавления: алерты поданы через API, PromQL в
# них не участвует. Отдельный вопрос — работает ли САМ ServiceDown на реальном
# исчезновении сервиса. Это разные отказы: выражение может быть написано так, что
# признак жизни не пропадает никогда (например, взята метрика без service_name
# или метрика, которую продолжает отдавать не сам сервис), и тогда подавление
# исправно, а гасить нечего.
#
# Поэтому здесь java-backend останавливается по-настоящему и проверяется цепочка:
# признак жизни пропал → алерт перешёл в firing → после подъёма всё вернулось.
#
# Фаза долгая: замеренная задержка обнаружения — 80-216 с в разных прогонах
# ОДНОЙ конфигурации (слагаемые перечислены в prometheus/rules/slo.yml, разброс —
# в results/08-alerts-findings.txt). Отсюда предел ожидания 420 с. Отключается
# переменной SKIP_SERVICEDOWN=1, но по умолчанию включена: без неё главный алерт
# остаётся непроверенным.
#
# Фаза предполагает БАЗОВЫЙ конфиг Collector. Варианты config-tail-sampling.yaml
# и config-high-cardinality.yaml задают тот же metric_expiration: 2m — если это
# разойдётся, задержка изменится, и разницу конфигурации легко принять за
# поведение алерта.

SD_ALIVE_GONE="пропущено"
SD_STATE="пропущено"
SD_RESTORED="пропущено"

if [ "${SKIP_SERVICEDOWN:-0}" = "1" ]; then
  log "ФАЗА 4: реальный ServiceDown — ПРОПУЩЕНА (SKIP_SERVICEDOWN=1)"
else
  log "ФАЗА 4: реальный ServiceDown (останавливаю java-backend, это долго)"
  # Флаг ставится ДО остановки, а не после: между командой и присваиванием тоже
  # можно получить сигнал, и тогда обработчик не знал бы, что чинить.
  JAVA_STOPPED=1
  docker compose stop java-backend >/dev/null 2>&1

  SD_LIMIT="${SD_LIMIT:-420}"
  sd_waited=0
  while [ "$sd_waited" -lt "$SD_LIMIT" ]; do
    ALIVE="$(prom_scalar 'slo:service:alive{service_name="java-backend"}')"
    # Признак жизни — ровно 1, пока сервис жив, и пустой результат, когда серия
    # исчезла. Промежуточных состояний у этого правила нет, поэтому сравнение
    # точное. Но пустой результат prom_scalar отдаёт как «0», а не «0.000000»:
    # форматирование применяется только к найденному значению. Первая версия
    # сравнивала лишь с «0.000000» и не увидела исчезновения серии ни разу —
    # проверка отработала весь предел в 420 с и объявила отказ на исправном
    # правиле. Ошибка в самой проверке выглядела как дефект стенда.
    if [ "$ALIVE" = "0" ] || [ "$ALIVE" = "0.000000" ]; then
      SD_ALIVE_GONE="да"
      break
    fi
    sd_waited=$((sd_waited + 10))
    sleep 10
  done
  echo "  признак жизни java-backend исчез: $SD_ALIVE_GONE (ждали ${sd_waited}с, предел ${SD_LIMIT}с)"

  if [ "$SD_ALIVE_GONE" = "да" ]; then
    # for: 1m плюс интервал вычисления — ждём с запасом.
    sd_fire=0
    SD_STATE="$(alert_state ServiceDown)"
    while [ "$sd_fire" -lt 120 ] && [ "$SD_STATE" != "firing" ]; do
      sleep 10
      sd_fire=$((sd_fire + 10))
      SD_STATE="$(alert_state ServiceDown)"
    done
    echo "  состояние ServiceDown: $SD_STATE (ожидается firing, ждали ${sd_fire}с)"
  else
    SD_ALIVE_GONE="нет"
    echo "  ОТКАЗ по существу: сервис остановлен, а признак жизни держится." >&2
    echo "         Значит выражение slo:service:alive опирается не на сам сервис." >&2
  fi

  docker compose start java-backend >/dev/null 2>&1
  # Флаг снимается сразу после команды подъёма, а не после подтверждения: с этого
  # момента чинить обработчику нечего, повторный `start` только запутал бы вывод.
  # Подтверждение ниже — уже про качество прогона, а не про аварию.
  JAVA_STOPPED=0
  # Возврат подтверждаем явно: если сервис не поднялся, все последующие прогоны
  # этого стенда будут врать, и лучше узнать об этом здесь.
  sd_back=0
  while [ "$sd_back" -lt 300 ]; do
    ALIVE="$(prom_scalar 'slo:service:alive{service_name="java-backend"}')"
    if [ "$ALIVE" = "1.000000" ]; then
      SD_RESTORED="да"
      break
    fi
    sd_back=$((sd_back + 10))
    sleep 10
  done
  [ "$SD_RESTORED" = "да" ] || SD_RESTORED="нет"
  echo "  java-backend вернулся: $SD_RESTORED (ждали ${sd_back}с)"
fi

# --- вердикт -----------------------------------------------------------------

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-40s %s (ожидалось %s)\n" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-40s %s (ожидалось %s)\n" "$name" "$got" "$want"
    FAILED=1
  fi
}

verdict_at_least() {
  local name="$1" got="$2" least="$3"
  if [ "$got" -ge "$least" ] 2>/dev/null; then
    printf "  ГОДЕН   %-40s %s (нужно не меньше %s)\n" "$name" "$got" "$least"
  else
    printf "  ОТКАЗ   %-40s %s (нужно не меньше %s)\n" "$name" "$got" "$least"
    FAILED=1
  fi
}

echo
log "вердикт"
verdict "состояние на деградации"              "$STATE_FIRING"      "firing"
# «Не меньше одного» здесь оправдано, в отличие от счёта сигналов: алертов с этим
# именем ровно столько, сколько сервисов деградировало, а деградируют оба —
# go-frontend отдаёт 502 потому, что java-backend отдал 500. Точное число
# зависело бы от того, успел ли второй сервис попасть в то же окно.
verdict_at_least "алертов в Alertmanager"      "$AM_COUNT"          1
verdict_at_least "уведомлений firing в приёмнике" "$WEBHOOK_FIRING" 1
verdict "недоставленных уведомлений"           "$FAILED_DELTA"      "0"
verdict "алерт погас после деградации"         "$(alert_state "$ALERT_NAME")" "inactive"
verdict_at_least "уведомлений resolved"        "$WEBHOOK_RESOLVED"  1
verdict "состояние на нормальной нагрузке"     "$WORST_STATE"       "inactive"
verdict "ложных уведомлений firing"            "$FALSE_FIRING"      "0"
verdict "фаза деградации воспроизвелась"       "$BAD_502"           "$BAD_REQUESTS"
verdict "подавление: цель того же сервиса"     "$INHIB_SAME"        "suppressed"
verdict "подавление: цель другого сервиса"     "$INHIB_OTHER"       "active"
verdict "подавление: источник не подавлен"     "$INHIB_SOURCE"      "active"
if [ "${SKIP_SERVICEDOWN:-0}" = "1" ]; then
  printf "  ПРОПУСК ServiceDown на реальной остановке (SKIP_SERVICEDOWN=1)\n"
else
  verdict "ServiceDown: признак жизни исчез"   "$SD_ALIVE_GONE"     "да"
  verdict "ServiceDown: состояние алерта"      "$SD_STATE"          "firing"
  verdict "ServiceDown: сервис вернулся"       "$SD_RESTORED"       "да"
fi

echo
if [ "$FAILED" = 0 ]; then
  echo "ИТОГ: алерт срабатывает на деградации, доезжает до приёмника и НЕ горит в покое."
else
  echo "ИТОГ: есть расхождения — смотри строки ОТКАЗ выше."
fi

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Самопроверка SLO-алертов"
  echo "========================"
  echo "Сценарий: $BAD_REQUESTS запросов с отказом сервера, затем $GOOD_REQUESTS нормальных."
  echo "Цель SLO 99%, порог быстрого алерта burn rate > 14.4 на окнах 5m и 10m, for: 30s."
  echo "ВНИМАНИЕ: окна стендовые. В проде это 5m/1h при окне SLO 30 дней."
  echo
  echo "Фаза 1 — деградация"
  echo "  получено 502: $BAD_502 из $BAD_REQUESTS"
  echo "  до firing: ${TIME_TO_FIRING}с ожидания, ${TOTAL_TO_FIRING}с от начала деградации"
  echo "  состояние в Prometheus: $STATE_FIRING"
  echo "  алертов в Alertmanager: $AM_COUNT"
  echo "  уведомлений в приёмнике: $WEBHOOK_TOTAL, из них firing: $WEBHOOK_FIRING"
  echo "  недоставленных уведомлений: $FAILED_DELTA"
  echo
  echo "Фаза 2 — покой и нормальная нагрузка"
  echo "  затухание: ${TIME_TO_QUIET}с"
  echo "  уведомлений resolved: $WEBHOOK_RESOLVED"
  echo "  успешных в нормальном прогоне: $GOOD_OK"
  echo "  худшее состояние за 90с наблюдения: $WORST_STATE"
  echo "  ложных уведомлений firing: $FALSE_FIRING"
  echo
  echo "Фаза 3 — подавление (синтетические алерты через API)"
  echo "  цель того же сервиса:  $INHIB_SAME (ожидается suppressed)"
  echo "  цель другого сервиса:  $INHIB_OTHER (ожидается active)"
  echo "  сам источник:          $INHIB_SOURCE (ожидается active)"
  echo
  echo "Фаза 4 — реальный ServiceDown (остановка java-backend)"
  echo "  признак жизни исчез:   $SD_ALIVE_GONE"
  echo "  состояние алерта:      $SD_STATE (ожидается firing)"
  echo "  сервис вернулся:       $SD_RESTORED"
  echo
  if [ "$FAILED" = 0 ]; then
    echo "ИТОГ: ГОДЕН"
  else
    echo "ИТОГ: ОТКАЗ"
  fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
