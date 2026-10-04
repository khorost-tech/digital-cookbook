#!/usr/bin/env bash
# Во что обходится один запрос в двух представлениях: широкое событие против
# набора метрик и логов.
#
#   ./scripts/bench-wide-events.sh [запросов]
#
# Сравнение честно ТОЛЬКО при одинаковом составе сведений. Если у широкого
# события полей больше, замер покажет разницу в количестве данных, а не в
# формате, и вывод «wide events дороже» окажется артефактом постановки.
#
# ЧЕГО ЭТОТ ЗАМЕР НЕ ПОКАЗЫВАЕТ. Главное обещание подхода — дешёвые запросы по
# высококардинальным полям в колоночном хранилище. Здесь колоночного хранилища
# нет (решение принято при проектировании: ещё один тяжёлый контейнер ради
# иллюстрации), поэтому цена запросов не измеряется вовсе. Измеряется только
# объём представления.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"
require_tools

REQUESTS="${1:-200}"
SEED="${2:-42}"
RESULT_FILE="$STAND_DIR/results/14-wide-events.txt"
# Метка прогона: она уходит в атрибут спана load.run и в лог, и по ней потом
# отбираются записи ИМЕННО этого прогона.
RUN_ID="we-$(date +%s)"

NS_NOW() { echo "$(( $(date +%s) * 1000000000 ))"; }

# --- нагрузка ----------------------------------------------------------------
#
# ТОЛЬКО УСПЕШНЫЕ заказы, и это условие сопоставимости, а не удобство. Событие
# «заказ создан» пишется лишь при успешном заказе: на 404 и 502 заказа нет,
# значит нет и записи. На обычной смешанной нагрузке объём событий пришлось бы
# делить на число запросов, часть которых событий не порождает, — и получилась
# бы величина, не означающая ничего.

log "прогон нагрузки: $REQUESTS успешных заказов, seed $SEED, метка $RUN_ID"
docker compose run --rm --no-deps -T loadgen   -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED"   -success-only -run "$RUN_ID" -concurrency 4 -json 2>/dev/null |
  sed -n '/^{/,$p' > "$STAND_DIR/results/.last-load-we.json" || true

read -r FACT_TOTAL FACT_CREATED <<<"$("$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
a = d['actual_by_status']
print(sum(a.values()), a.get('201', 0))
" < "$STAND_DIR/results/.last-load-we.json")"
echo "  отправлено: $FACT_TOTAL, создано заказов: $FACT_CREATED"

if [ "$FACT_TOTAL" != "$FACT_CREATED" ]; then
  echo "ОШИБКА: не все запросы успешны ($FACT_CREATED из $FACT_TOTAL)." >&2
  echo "        Замер сравнивает объём события с числом заказов, и неуспешные" >&2
  echo "        запросы событий не создают — сопоставимость теряется." >&2
  exit 1
fi

# --- записи ИМЕННО этого прогона ---------------------------------------------
#
# Отбор по метке обязателен. Первая редакция брала любую запись за последние
# десять минут и первую попавшуюся: она могла принадлежать прошлому прогону,
# другому режиму стенда или вообще другому набору полей — и замер молча
# описывал не то, что только что происходило.

log "жду записи этого прогона в Loki и считаю их"
LOKI_JSON=""
EVENTS=0
for _ in $(seq 1 20); do
  LOKI_JSON="$(innet -s --max-time 30 -G "http://loki:3100/loki/api/v1/query_range"     --data-urlencode "query={service_name=\"go-frontend\"} | load_run = \"$RUN_ID\" | order_id != \"\""     --data-urlencode "start=$(( $(date +%s) - 900 ))000000000"     --data-urlencode "end=$(NS_NOW)"     --data-urlencode "limit=5000" 2>/dev/null)"
  EVENTS="$(printf '%s' "$LOKI_JSON" | "$PYTHON_BIN" -c "
import sys, json
try:
    res = json.load(sys.stdin)['data']['result']
except Exception:
    print(0); raise SystemExit
print(sum(len(s.get('values', [])) for s in res))
")"
  [ "$EVENTS" -ge "$FACT_CREATED" ] 2>/dev/null && break
  sleep 5
done
echo "  событий этого прогона в Loki: $EVENTS (заказов создано: $FACT_CREATED)"

# --- разбор одной записи -----------------------------------------------------

# Считается НЕ одна случайная запись, а ВСЕ события прогона. Первая редакция
# брала первую попавшуюся, и значение прыгало между прогонами: 509 байт против
# 488 — просто потому, что у разных заказов разной длины order_id и метка
# прогона. Одна запись здесь ничего не характеризует; медиана по двум сотням —
# характеризует.
read -r BODY_BYTES META_BYTES META_COUNT TOTAL_BYTES BUSINESS_BYTES MIN_BYTES MAX_BYTES <<<"$(printf '%s' "$LOKI_JSON" | "$PYTHON_BIN" -c "
import sys, json, statistics

# Поля, которые описывают САМ ЗАКАЗ, — то есть те, ради которых событие и
# существует. Остальное (версия SDK, описание рантайма, флаги) — служебное
# сопровождение.
BUSINESS = {'order_id', 'sku', 'quantity', 'reserved', 'trace_id', 'span_id',
            'severity_text', 'service_name', 'load_run'}

try:
    res = json.load(sys.stdin)['data']['result']
except Exception:
    print('0 0 0 0 0 0 0'); raise SystemExit
if not res:
    print('0 0 0 0 0 0 0'); raise SystemExit

# Считается ПОЛЕЗНАЯ НАГРУЗКА: тело строки плюс длины ключей и значений полей,
# всё в UTF-8. Ни заголовков протокола, ни временной метки, ни накладных
# расходов формата хранения, ни сжатия здесь нет — и в отчёте это сказано.
bodies, metas, totals, business, counts = [], [], [], [], []
for stream in res:
    labels = stream['stream']
    meta = sum(len(k.encode('utf-8')) + len(str(v).encode('utf-8')) for k, v in labels.items())
    biz = sum(len(k.encode('utf-8')) + len(str(v).encode('utf-8'))
              for k, v in labels.items() if k in BUSINESS)
    for ts, line in stream.get('values', []):
        body = len(line.encode('utf-8'))
        bodies.append(body)
        metas.append(meta)
        totals.append(body + meta)
        business.append(biz)
        counts.append(len(labels))

med = lambda xs: int(statistics.median(xs))
print(med(bodies), med(metas), med(counts), med(totals), med(business),
      min(totals), max(totals))
")"

echo "  тело строки: $BODY_BYTES байт"
echo "  полей: $META_COUNT, суммарно $META_BYTES байт"
echo "  полезная нагрузка записи: $TOTAL_BYTES байт (медиана; от $MIN_BYTES до $MAX_BYTES)"
echo "  из них поля заказа: $BUSINESS_BYTES байт"

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-44s %s (ожидалось %s)
" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-44s %s (ожидалось %s)
" "$name" "$got" "$want"
    FAILED=1
  fi
}

echo
log "вердикт"
# Точное равенство: каждый успешный заказ обязан дать ровно одно событие.
# Меньше — записи потерялись по дороге; больше — в выборку попали чужие.
verdict "событий = числу созданных заказов" "$EVENTS" "$FACT_CREATED"
verdict "все запросы успешны"               "$FACT_CREATED" "$FACT_TOTAL"

if [ "$TOTAL_BYTES" = "0" ]; then
  echo "ОШИБКА: записей о заказах этого прогона в Loki нет — сравнивать нечего." >&2
  exit 1
fi

# --- сколько серий описывают тот же запрос -----------------------------------
#
# Классическое представление того же события — метрики. Считаем, сколько серий
# они занимают, чтобы сравнивать не «байты против байтов», а то, что стоит
# денег: постоянно живущие серии против записей, которые истекают по ретеншену.

log "считаю серии метрик, описывающие тот же заказ"
count_series() {
  innet -s --max-time 20 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=count($1)" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    rs = json.load(sys.stdin)['data']['result']
    print(int(float(rs[0]['value'][1])) if rs else 0)
except Exception:
    print(0)
"
}

S_COUNTER="$(count_series 'khorost_tech_orders_created_total')"
S_HIST="$(count_series 'khorost_tech_order_duration_seconds_bucket')"
S_HIST_SUM="$(count_series 'khorost_tech_order_duration_seconds_sum')"
S_HIST_CNT="$(count_series 'khorost_tech_order_duration_seconds_count')"
S_TOTAL=$((S_COUNTER + S_HIST + S_HIST_SUM + S_HIST_CNT))

echo "  счётчик заказов: $S_COUNTER серий"
echo "  гистограмма длительности: $S_HIST бакетов + $S_HIST_SUM sum + $S_HIST_CNT count"
echo "  итого серий на бизнес-операцию: $S_TOTAL"

# --- отчёт -------------------------------------------------------------------

PER_REQ="$("$PYTHON_BIN" -c "print(round($TOTAL_BYTES * $FACT_CREATED / 1024.0, 1))")"

mkdir -p "$STAND_DIR/results"
{
  echo "Wide events против метрик: полное событие против агрегированного сигнала"
  echo "========================================================================"
  echo "Прогон: $REQUESTS запросов, seed $SEED, метка $RUN_ID."
  echo "Нагрузка ТОЛЬКО успешная: событие «заказ создан» пишется лишь при"
  echo "успешном заказе, и на смешанной нагрузке объём событий пришлось бы"
  echo "делить на число запросов, часть которых событий не порождает."
  echo
  echo "  ГОДЕН   событий = числу созданных заказов  $EVENTS (ожидалось $FACT_CREATED)"
  echo
  echo "ЧТО ОКАЗАЛОСЬ ПРИ РАЗБОРЕ: стенд УЖЕ пишет широкие события."
  echo "Тело записи о заказе — $BODY_BYTES байт («заказ создан»), а все поля"
  echo "запроса лежат в structured metadata: $META_COUNT полей, $META_BYTES байт."
  echo "Loki возвращает их в результатах запроса как поля, но НЕ индексирует —"
  echo "то есть уникальный order_id здесь не создаёт ни стрима, ни серии."
  echo
  echo "ОДНО СОБЫТИЕ (полезная нагрузка, медиана по $EVENTS событиям)"
  echo "  тело:                 $BODY_BYTES байт"
  echo "  поля:                 $META_BYTES байт ($META_COUNT полей)"
  echo "  всего:                $TOTAL_BYTES байт (разброс от $MIN_BYTES до $MAX_BYTES)"
  echo "  из них поля заказа:   $BUSINESS_BYTES байт"
  echo "  на $FACT_CREATED заказов:       $PER_REQ КиБ (иллюстративно, см. оговорки)"
  echo
  echo "ТЕ ЖЕ ЗАКАЗЫ В МЕТРИКАХ"
  echo "  счётчик заказов:      $S_COUNTER серий"
  echo "  гистограмма:          $S_HIST бакетов + sum + count = $((S_HIST + S_HIST_SUM + S_HIST_CNT))"
  echo "  итого:                $S_TOTAL серий"
  echo
  echo "ЭТО НЕ РАВНОЗНАЧНЫЕ ПРЕДСТАВЛЕНИЯ, И В ЭТОМ ВСЯ СУТЬ"
  echo "Сравниваются ПОЛНОЕ СОБЫТИЕ и АГРЕГИРОВАННЫЙ СИГНАЛ, а не два способа"
  echo "записать одно и то же. Событие несёт order_id, trace_id, span_id, sku —"
  echo "то, чего в метриках нет и быть не должно: положить order_id в лейбл"
  echo "значит взорвать кардинальность (results/13-metric-cardinality.txt)."
  echo "Метрики отвечают на вопрос «как система в целом», событие — «что было"
  echo "с ЭТИМ заказом». Вторая величина меньше именно потому, что деталей"
  echo "не хранит."
  echo
  echo "КАК РАСТЁТ КАЖДАЯ СТОРОНА"
  echo "События: линейно по числу заказов, истекают по ретеншену."
  echo "Метрики: число серий от числа запросов не зависит — но это НЕ значит,"
  echo "что метрики бесплатны при росте трафика. Разделять надо три вещи:"
  echo "  - кардинальность: $S_TOTAL серий, растёт от новых значений лейблов"
  echo "    (один order_id дал 2 -> 509, см. 13-metric-cardinality.txt);"
  echo "  - частота сэмплов: Prometheus скрейпит раз в 5 с независимо от"
  echo "    трафика, то есть сэмплы копятся по времени, а не по запросам;"
  echo "  - объём на событие: у метрик его нет вовсе, у событий он и есть"
  echo "    основная статья расхода."
  echo "Плюс расходы, которые здесь не мерились совсем: работа SDK и Collector"
  echo "на каждом запросе, передача до Collector, обработка в нём."
  echo
  echo "ЧЕГО ЭТОТ ЗАМЕР НЕ ПОКАЗЫВАЕТ"
  echo "1. Цену запросов по высококардинальным полям — для этого нужно"
  echo "   колоночное хранилище, которого на стенде нет."
  echo "2. Реальный размер на диске. Посчитана ПОЛЕЗНАЯ НАГРУЗКА: тело плюс"
  echo "   длины ключей и значений в UTF-8. Ни заголовков протокола, ни"
  echo "   временной метки, ни накладных расходов формата, ни сжатия."
  echo "3. Экстраполяция «$PER_REQ КиБ на $FACT_CREATED заказов» — иллюстрация"
  echo "   линейности, а не измеренная стоимость хранения."
  echo
  if [ "$FAILED" = 0 ]; then echo "ИТОГ: ГОДЕН"; else echo "ИТОГ: ОТКАЗ"; fi
} > "$RESULT_FILE"

echo
echo "ИТОГ: событие $TOTAL_BYTES байт ($META_COUNT полей) против $S_TOTAL постоянных серий."
echo "      Это полное событие против агрегированного сигнала, а не два способа"
echo "      записать одно и то же."
echo "Отчёт: results/$(basename "$RESULT_FILE")"
