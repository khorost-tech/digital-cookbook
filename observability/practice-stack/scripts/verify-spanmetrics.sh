#!/usr/bin/env bash
# Самопроверка span-метрик: означают ли они то же, что метрики RED статьи 4.
#
#   ./scripts/verify-spanmetrics.sh [запросов] [seed]
#   ./scripts/verify-spanmetrics.sh sampling [запросов]   # опыт с сэмплингом
#
# Требует режима: ./scripts/up.sh all spanmetrics
#
# Появление span-метрик само по себе не доказывает НИЧЕГО. Их считают из спанов,
# а RED-метрики статьи 4 — из HTTP-инструментирования; если числа расходятся,
# врёт один из двух источников, и молча. Поэтому проверка сравнивает смысл, а не
# наличие.
#
# Дельты, а не rate-окна. Счётчик до прогона и после даёт точное число событий;
# rate за окно зависит от того, когда именно был скрейп, и точное равенство на
# нём не построить.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"
require_tools

MODE_ARG="${1:-run}"
if [ "$MODE_ARG" = "sampling" ]; then
  REQUESTS="${2:-1000}"
  SEED=42
else
  REQUESTS="${1:-200}"
  SEED="${2:-42}"
fi
RESULT_FILE="$STAND_DIR/results/11-spanmetrics.txt"
LOAD_JSON="$STAND_DIR/results/.last-load-spanmetrics.json"

# --- запросы к Prometheus ----------------------------------------------------

# Сумма значений по всем сериям. Пустой результат отдаётся как EMPTY: отсутствие
# метрики и честный ноль — разные диагнозы, и путать их нельзя.
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

# То же, но пустой результат — ноль. Для счётчиков, которых может не быть вовсе:
# у сервиса без единой ошибки серии с status_code=ERROR не существует.
prom_num() {
  local v
  v="$(prom_sum "$1")"
  if [ "$v" = "EMPTY" ] || [ "$v" = "ERR" ]; then echo 0; else echo "$v"; fi
}

# Ждать, пока метрика появится. Нужна отдельно для пути через Tempo: генератор
# отправляет метрики по remote_write пачками, и сразу после нагрузки их ещё нет.
wait_metric() {
  local query="$1" tries="${2:-30}" i=0 v
  while [ "$i" -lt "$tries" ]; do
    v="$(prom_sum "$query")"
    [ "$v" != "EMPTY" ] && [ "$v" != "ERR" ] && { echo "$v"; return 0; }
    i=$((i + 1))
    sleep 3
  done
  echo "EMPTY"
  return 1
}

# Ждать, пока дельта достигнет ОЖИДАЕМОГО значения. Не стабилизации и не
# фиксированного времени.
#
# Обе прежние редакции этого ожидания были неверны, и обе давали ложный отказ на
# исправном стенде:
#
#   1) фиксированные 25 секунд — Tempo не успевал (у него свой интервал сбора
#      15 с плюс отправка по remote_write пачками), дельта выходила нулевой;
#   2) «два одинаковых замера подряд» — ещё хуже: при интервале скрейпа 5 с два
#      запроса с паузой 5 с регулярно попадают в один и тот же скрейп, и
#      «значение стабильно» означает «нового скрейпа не было», а вовсе не
#      «данные доехали». Проверка объявляла ОТКАЗ ровно там, где данные были в
#      пути.
#
# Здесь известно, сколько именно должно приехать, поэтому ждём этого числа.
# Не дождались за предел — честный отказ с понятной причиной.
wait_delta() {
  local query="$1" before="$2" expected="$3" limit="${4:-40}" i=0 cur delta
  while [ "$i" -lt "$limit" ]; do
    cur="$(prom_num "$query")"
    delta="$("$PYTHON_BIN" -c "print(round($cur - $before))")"
    if [ "$delta" -ge "$expected" ] 2>/dev/null; then
      echo "$delta"
      return 0
    fi
    i=$((i + 1))
    sleep 5
  done
  echo "$delta"
  return 1
}

# --- запросы, общие для обоих генераторов ------------------------------------
#
# ВНИМАНИЕ на имена: у двух генераторов они РАЗНЫЕ, и это не опечатка.
#   Collector: traces_span_metrics_calls_total, лейбл сервиса service_name
#   Tempo:     traces_spanmetrics_calls_total,  лейбл сервиса service
# Разница задокументирована в results/00-wave3-feasibility.txt: дашборд,
# написанный на одном пути, на другом не работает.

Q_COL_ALL='sum(traces_span_metrics_calls_total{span_kind="SPAN_KIND_SERVER",service_name="go-frontend",span_name="POST /order"})'
Q_COL_ERR='sum(traces_span_metrics_calls_total{span_kind="SPAN_KIND_SERVER",service_name="go-frontend",span_name="POST /order",status_code="STATUS_CODE_ERROR"})'
Q_TEM_ALL='sum(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER",service="go-frontend",span_name="POST /order"})'
Q_TEM_ERR='sum(traces_spanmetrics_calls_total{span_kind="SPAN_KIND_SERVER",service="go-frontend",span_name="POST /order",status_code="STATUS_CODE_ERROR"})'

# --- контрольная точка -------------------------------------------------------
#
# Проверить, что оба генератора вообще работают, ДО того как судить по числам.
# Без этого нулевая дельта означала бы и «метрики не считаются», и «нагрузка не
# дошла» — два разных отказа с одинаковым видом.

# ПРОГРЕВ. На чистом стенде span-метрик нет вовсе: они появляются только из
# спанов, а спаны — только из запросов. Контрольная точка ниже без прогрева
# упирается в тупик: она ждёт метрик, которые появятся лишь после нагрузки,
# создаваемой этой же проверкой ПОЗЖЕ. Проверка отказывала на исправном стенде
# сразу после down.sh clean.
WARM_REQUESTS=60
# Значение ДО прогрева нужно, чтобы дождаться доставки самого прогрева: число
# его запросов известно, значит известна и ожидаемая дельта.
WARM_BEFORE_COL="$(prom_num "$Q_COL_ALL")"
WARM_BEFORE_TEM="$(prom_num "$Q_TEM_ALL")"

log "прогрев: создаю спаны, иначе на чистом стенде метрик нет вовсе"
docker compose run --rm --no-deps -T loadgen   -target http://go-frontend:8080 -requests "$WARM_REQUESTS" -seed 1 -concurrency 4 >/dev/null 2>&1 || true

log "контрольная точка: оба генератора отдают метрики"
CTRL_COL="$(wait_metric "$Q_COL_ALL" 20)" || true
CTRL_TEM="$(wait_metric "$Q_TEM_ALL" 20)" || true
echo "  Collector: $CTRL_COL"
echo "  Tempo:     $CTRL_TEM"

if [ "$CTRL_COL" = "EMPTY" ]; then
  echo "ОШИБКА: span-метрик Collector нет. Стенд поднят не в режиме spanmetrics?" >&2
  echo "        Нужно: ./scripts/up.sh all spanmetrics" >&2
  exit 1
fi
if [ "$CTRL_TEM" = "EMPTY" ]; then
  echo "ОШИБКА: метрик генератора Tempo нет. Проверь, что в overrides включены" >&2
  echo "        процессоры: без секции overrides.defaults.metrics_generator" >&2
  echo "        генератор стартует без ошибок и молча ничего не делает." >&2
  exit 1
fi

# --- опыт с сэмплингом -------------------------------------------------------
#
# Главный опыт темы, поэтому вынесен в отдельный режим: он требует перезапуска
# приложений и большого числа запросов.
#
# Почему 1000, а не 200: волна 1 замерила фактическую долю при заявленных 10 % —
# 13,5 % и 14,0 % на двух прогонах по 200 запросов против 11,8 % на 1000
# (results/03-tracing.txt). На сотнях запросов разброс биномиального
# распределения того же порядка, что измеряемая величина, и вывод о множителе
# был бы выдуманным.
if [ "$MODE_ARG" = "sampling" ]; then
  log "ОПЫТ: span-метрики при head-сэмплинге 10 % ($REQUESTS запросов)"

  # Вывод подъёма уходит в файл, а НЕ в /dev/null: при подавленном выводе
  # ненулевой код обрывал скрипт под `set -e` молча — в логе оставалась одна
  # строка «ОПЫТ» без единого намёка на причину.
  UP_LOG="$STAND_DIR/results/.last-up-sampling.log"
  # export, а не префикс перед командой. Префикс достаётся самому up.sh, но до
  # `docker compose` внутри него доходил не всегда: в одном прогоне контейнер
  # поднялся с ПУСТЫМИ переменными, сэмплирование не применилось, и проверка
  # отрапортовала «множитель 1.0x» — как результат опыта. Настоящий множитель
  # на этом же стенде 9.8x.
  export OTEL_TRACES_SAMPLER=traceidratio
  export OTEL_TRACES_SAMPLER_ARG=0.1
  if ! ./scripts/up.sh all spanmetrics > "$UP_LOG" 2>&1; then
    echo "ОШИБКА: подъём стенда с сэмплером не удался. Хвост лога:" >&2
    tail -15 "$UP_LOG" >&2
    exit 1
  fi

  # КОНТРОЛЬ СОСТОЯТЕЛЬНОСТИ ОПЫТА. Без него «сэмплирование не применилось»
  # неотличимо от «сэмплирование ничего не изменило», а это противоположные
  # выводы: первое — сломанный опыт, второе — открытие.
  SAMPLER_IN_CONTAINER="$(docker exec ops-go-frontend env 2>/dev/null | grep '^OTEL_TRACES_SAMPLER=' | cut -d= -f2- || true)"
  echo "  сэмплер внутри контейнера: '${SAMPLER_IN_CONTAINER:-ПУСТО}'"
  if [ "$SAMPLER_IN_CONTAINER" != "traceidratio" ]; then
    echo "ОШИБКА: приложение поднялось БЕЗ сэмплера — опыт не состоялся." >&2
    echo "        Любое число, полученное дальше, описывало бы обычный режим." >&2
    exit 1
  fi

  # После пересоздания приложений счётчик начинается заново: сравнивать с
  # прежним значением нельзя, поэтому база снимается ПОСЛЕ подъёма.
  sleep 15
  BEFORE_S="$(prom_num "$Q_COL_ALL")"

  docker compose run --rm --no-deps -T loadgen \
    -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED" \
    -concurrency 4 -json 2>/dev/null | sed -n '/^{/,$p' > "$LOAD_JSON" || true
  # Сколько именно доедет — заранее неизвестно (в том и опыт), поэтому здесь
  # ждём фиксированно: доля сэмплирования не даёт ожидаемого числа.
  sleep 60
  AFTER_S="$(prom_num "$Q_COL_ALL")"

  SAMPLED="$("$PYTHON_BIN" -c "print(round($AFTER_S - $BEFORE_S))")"
  FACTOR="$("$PYTHON_BIN" -c "print(round($REQUESTS / max($SAMPLED, 1), 2))")"
  SHARE="$("$PYTHON_BIN" -c "print(round(100.0 * $SAMPLED / $REQUESTS, 1))")"

  echo "  запросов отправлено:      $REQUESTS"
  echo "  учтено span-метриками:    $SAMPLED (${SHARE} %)"
  echo "  множитель занижения:      ${FACTOR}x"

  # Второй контроль, уже по результату: при работающем сэмплировании 10 % доля
  # обязана быть заметно ниже 100 %. Значение около сотни означает, что сэмплер
  # не сработал, и выдавать его за «множитель 1.0x» нельзя.
  if "$PYTHON_BIN" -c "import sys; sys.exit(0 if $SHARE > 50 else 1)" 2>/dev/null; then
    echo "ОШИБКА: учтено ${SHARE} % — это не похоже на сэмплирование 10 %." >&2
    echo "        Опыт не состоялся; число не является результатом." >&2
    exit 1
  fi

  echo
  echo "  Метрика посчитана по тому, что дошло. Это не поломка конвейера, а"
  echo "  свойство: при head-сэмплинге span-метрики систематически занижают"
  echo "  объём, и восстановить настоящее число по ним нельзя."

  # Возврат стенда в обычный режим — иначе следующий прогон любой проверки
  # получит сэмплированный поток и объяснит расхождение чем угодно, кроме
  # настоящей причины. Переменные снимаются, иначе они утекут в этот подъём.
  unset OTEL_TRACES_SAMPLER OTEL_TRACES_SAMPLER_ARG
  ./scripts/up.sh all spanmetrics > "$UP_LOG" 2>&1 || \
    echo "ВНИМАНИЕ: стенд не вернулся в обычный режим, следующий прогон будет врать" >&2

  {
    echo "Опыт: span-метрики при head-сэмплинге 10 %"
    echo "==========================================="
    echo "Запросов: $REQUESTS, seed $SEED."
    echo "  учтено span-метриками: $SAMPLED (${SHARE} %)"
    echo "  множитель занижения:   ${FACTOR}x"
  } >> "$RESULT_FILE"
  echo "Дописано в: results/$(basename "$RESULT_FILE")"
  exit 0
fi

# --- замер: дельта по обоим генераторам --------------------------------------

# Перед снятием базы — пауза без нагрузки: хвост прошлого трафика должен
# доехать по ОБОИМ путям, иначе он попадёт в дельту этого прогона. Пауза равна
# интервалу сбора генератора Tempo (15 с) с запасом.
# Ждём доставки ПРОГРЕВА, а не фиксированное время. Пауза «на 45 секунд»
# выглядела достаточной и не была: прогревочные 60 запросов доехали до Collector
# уже ПОСЛЕ снятия базы, и проверка объявила «учтено 260 (ожидалось 200)» —
# лишними оказались ровно её собственные прогревочные запросы.
#
# Тот же дефект в этом стенде есть и у verify-metrics (см.
# results/07-metrics-findings.txt): если проверка сама создаёт нагрузку до
# замера, между ними обязано быть ожидание доставки, а не пауза на глазок.
log "база до прогона: жду, пока доедут все $WARM_REQUESTS прогревочных запроса"
WARM_GOT_COL="$(wait_delta "$Q_COL_ALL" "$WARM_BEFORE_COL" "$WARM_REQUESTS" 40)" ||   echo "  ВНИМАНИЕ: Collector получил только $WARM_GOT_COL из $WARM_REQUESTS прогревочных" >&2
WARM_GOT_TEM="$(wait_delta "$Q_TEM_ALL" "$WARM_BEFORE_TEM" "$WARM_REQUESTS" 40)" ||   echo "  ВНИМАНИЕ: Tempo получил только $WARM_GOT_TEM из $WARM_REQUESTS прогревочных" >&2
echo "  прогрев доехал: Collector $WARM_GOT_COL, Tempo $WARM_GOT_TEM"
BEFORE_COL_ALL="$(prom_num "$Q_COL_ALL")"
BEFORE_COL_ERR="$(prom_num "$Q_COL_ERR")"
BEFORE_TEM_ALL="$(prom_num "$Q_TEM_ALL")"
BEFORE_TEM_ERR="$(prom_num "$Q_TEM_ERR")"
echo "  Collector: всего $BEFORE_COL_ALL, ошибок $BEFORE_COL_ERR"
echo "  Tempo:     всего $BEFORE_TEM_ALL, ошибок $BEFORE_TEM_ERR"

log "прогон нагрузки: $REQUESTS запросов, seed $SEED"
docker compose run --rm --no-deps -T loadgen \
  -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED" \
  -concurrency 4 -json 2>/dev/null | sed -n '/^{/,$p' > "$LOAD_JSON" || true

# Факт нагрузки — эталон, с которым сверяется всё остальное. Берётся из отчёта
# генератора, а не из ожиданий: нормальная нагрузка НАМЕРЕННО содержит 404 и 502.
read -r FACT_TOTAL FACT_2XX FACT_404 FACT_502 <<<"$("$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
a = d['actual_by_status']
tot = sum(a.values())
print(tot,
      sum(v for k, v in a.items() if k.startswith('2')),
      a.get('404', 0),
      a.get('502', 0))
" < "$LOAD_JSON")"
echo "  фактически: всего $FACT_TOTAL, 2xx $FACT_2XX, 404 $FACT_404, 502 $FACT_502"

# Ждём не время, а стабилизацию: у путей разная задержка доставки, и любой
# фиксированный интервал либо слишком мал для Tempo, либо тратит время впустую.
log "жду, пока обоими путями доедут все $FACT_TOTAL запросов"
D_COL_WAIT="$(wait_delta "$Q_COL_ALL" "$BEFORE_COL_ALL" "$FACT_TOTAL" 40)" ||   echo "  ВНИМАНИЕ: Collector доставил только $D_COL_WAIT из $FACT_TOTAL" >&2
D_TEM_WAIT="$(wait_delta "$Q_TEM_ALL" "$BEFORE_TEM_ALL" "$FACT_TOTAL" 40)" ||   echo "  ВНИМАНИЕ: Tempo доставил только $D_TEM_WAIT из $FACT_TOTAL" >&2
echo "  доехало: Collector $D_COL_WAIT, Tempo $D_TEM_WAIT"

AFTER_COL_ALL="$(prom_num "$Q_COL_ALL")"
AFTER_COL_ERR="$(prom_num "$Q_COL_ERR")"
AFTER_TEM_ALL="$(prom_num "$Q_TEM_ALL")"
AFTER_TEM_ERR="$(prom_num "$Q_TEM_ERR")"

D_COL_ALL="$("$PYTHON_BIN" -c "print(round($AFTER_COL_ALL - $BEFORE_COL_ALL))")"
D_COL_ERR="$("$PYTHON_BIN" -c "print(round($AFTER_COL_ERR - $BEFORE_COL_ERR))")"
D_TEM_ALL="$("$PYTHON_BIN" -c "print(round($AFTER_TEM_ALL - $BEFORE_TEM_ALL))")"
D_TEM_ERR="$("$PYTHON_BIN" -c "print(round($AFTER_TEM_ERR - $BEFORE_TEM_ERR))")"

echo "  дельта Collector: всего $D_COL_ALL, ошибок $D_COL_ERR"
echo "  дельта Tempo:     всего $D_TEM_ALL, ошибок $D_TEM_ERR"

# --- сверка с RED-метриками статьи 4 -----------------------------------------
#
# Доля ошибок двумя независимыми путями. RED считает по HTTP-инструментированию
# (otelhttp), span-метрики — по статусам спанов. Совпадение не гарантировано
# устройством: это разные источники, и если они разойдутся, дашборды серии
# начнут показывать разное.

log "сверка доли ошибок с RED-метриками статьи 4"
RED_RATIO="$(prom_num 'job:http_server_requests_errors:ratio5m{service_name="go-frontend"}')"
SPAN_RATIO="$("$PYTHON_BIN" -c "print(round($D_COL_ERR / max($D_COL_ALL, 1), 4))")"
FACT_RATIO="$("$PYTHON_BIN" -c "print(round($FACT_502 / max($FACT_TOTAL, 1), 4))")"
echo "  доля ошибок по факту нагрузки: $FACT_RATIO"
echo "  доля ошибок по span-метрикам:  $SPAN_RATIO"
echo "  доля ошибок по RED (rate5m):   $RED_RATIO"

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

# Отдельная форма вердикта для величин, у которых точное равенство недостижимо
# по устройству. Допуск ОБЯЗАН быть назван вслух вместе с причиной: «примерно
# совпало» без границы — это не проверка, а впечатление.
verdict_close() {
  local name="$1" got="$2" want="$3" tol="$4"
  if "$PYTHON_BIN" -c "import sys; sys.exit(0 if abs($got - $want) <= $tol else 1)" 2>/dev/null; then
    printf "  ГОДЕН   %-42s %s (эталон %s, допуск %s)\n" "$name" "$got" "$want" "$tol"
  else
    printf "  ОТКАЗ   %-42s %s (эталон %s, допуск %s)\n" "$name" "$got" "$want" "$tol"
    FAILED=1
  fi
}

echo
log "вердикт"
verdict "Collector: учтено запросов"        "$D_COL_ALL" "$FACT_TOTAL"
verdict "Tempo: учтено запросов"            "$D_TEM_ALL" "$FACT_TOTAL"
verdict "два генератора согласны по объёму" "$D_COL_ALL" "$D_TEM_ALL"
# 502 — отказ сервера, спан размечен статусом ошибки. 404 в число ошибок НЕ
# входит: ненайденный товар — не отказ сервиса, и в бюджет ошибок SLO статьи 5
# он тоже не попадает. Проверка закрепляет это соответствие числом.
verdict "Collector: ошибок = число 502"     "$D_COL_ERR" "$FACT_502"
verdict "Tempo: ошибок = число 502"         "$D_TEM_ERR" "$FACT_502"
verdict "два генератора согласны по ошибкам" "$D_COL_ERR" "$D_TEM_ERR"
verdict_close "доля ошибок: span против факта" "$SPAN_RATIO" "$FACT_RATIO" 0.005
# Допуск для RED шире: это rate за окно 5 минут, куда попадает не только текущий
# прогон, но и хвост предыдущего трафика. Сравнение имеет смысл как проверка
# порядка величины, а не как точное равенство.
verdict_close "доля ошибок: RED против факта"  "$RED_RATIO"  "$FACT_RATIO" 0.03

echo
if [ "$FAILED" = 0 ]; then
  echo "ИТОГ: span-метрики обоих генераторов согласны с фактом нагрузки и с RED."
else
  echo "ИТОГ: есть расхождения — смотри строки ОТКАЗ выше."
fi

# --- отчёт -------------------------------------------------------------------

mkdir -p "$STAND_DIR/results"
{
  echo "Самопроверка span-метрик"
  echo "========================"
  echo "Прогон: $REQUESTS запросов, seed $SEED."
  echo "Режим стенда: spanmetrics (коннектор Collector + генератор Tempo одновременно)."
  echo
  echo "Факт нагрузки: всего $FACT_TOTAL, 2xx $FACT_2XX, 404 $FACT_404, 502 $FACT_502"
  echo
  echo "Учтено span-метриками:"
  echo "  Collector: всего $D_COL_ALL, ошибок $D_COL_ERR"
  echo "  Tempo:     всего $D_TEM_ALL, ошибок $D_TEM_ERR"
  echo
  echo "Доля ошибок:"
  echo "  по факту нагрузки: $FACT_RATIO"
  echo "  по span-метрикам:  $SPAN_RATIO"
  echo "  по RED (rate5m):   $RED_RATIO"
  echo
  echo "ВНИМАНИЕ: 404 не считается ошибкой ни спаном, ни RED-правилом."
  echo "Ошибками признаются только 502 — отказы сервера."
  echo
  if [ "$FAILED" = 0 ]; then echo "ИТОГ: ГОДЕН"; else echo "ИТОГ: ОТКАЗ"; fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
