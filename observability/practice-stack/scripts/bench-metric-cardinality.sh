#!/usr/bin/env bash
# Замер взрыва кардинальности МЕТРИК и цены резки в пайплайне.
#
#   ./scripts/bench-metric-cardinality.sh [запросов] [предел_order_id]
#
# Три прогона, различающихся ровно одним:
#   base — обычный стенд, у бизнес-счётчика один низкокардинальный лейбл;
#   bomb — приложение вешает в лейбл уникальный order_id;
#   cut  — тот же взрыв, но Collector сворачивает лейбл обратно.
#
# Между прогонами тома чистятся. Prometheus помнит серию и после того, как та
# перестала обновляться, поэтому остаточные серии прошлого прогона исказили бы
# и счёт, и память — а счёт здесь главная величина.
#
# ЧТО ЗДЕСЬ ДОКАЗАТЕЛЬНО: число серий. Оно воспроизводится и объясняется
# арифметикой. Память и время запроса на стендовом объёме — почти наверняка шум,
# и отчёт говорит об этом прямо, а не оставляет читателю додумывать.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"
require_tools

REQUESTS="${1:-600}"
BOMB_MAX="${2:-500}"
SEED=42
RESULT_FILE="$STAND_DIR/results/13-metric-cardinality.txt"
LOAD_JSON="$STAND_DIR/results/.last-load-cardinality.json"
METRIC="khorost_tech_orders_created_total"

# --- запросы -----------------------------------------------------------------

prom_scalar() {
  innet -s --max-time 20 -G "http://prometheus:9090/api/v1/query" \
    --data-urlencode "query=$1" 2>/dev/null |
    "$PYTHON_BIN" -c "
import sys, json
try:
    rs = json.load(sys.stdin)['data']['result']
    # Целое, а не float: все величины здесь — счётные (серии, сумма счётчика),
    # а вердикт сравнивает СТРОКИ. Первая редакция печатала round(x, 3), и
    # проверка объявляла ОТКАЗ на совпавших значениях: '2.0' против '2'.
    v = float(rs[0]['value'][1]) if rs else 0
    print(int(v) if v == int(v) else round(v, 3))
except Exception:
    print(0)
"
}

# Память контейнера в МиБ. Единицы приводим сами: docker stats печатает то МиБ,
# то ГиБ, и сравнивать строки бессмысленно.
mem_mib() {
  docker stats --no-stream --format "{{.MemUsage}}" "$1" 2>/dev/null |
    awk '{ split($0, a, " / "); v=a[1]; u=v;
           gsub(/[A-Za-z]+/,"",v); gsub(/[0-9.]+/,"",u);
           if (u=="GiB") v=v*1024; printf "%.1f", v+0 }'
}

# Медиана пяти замеров времени запроса. Одиночный замер на таком объёме
# бесполезен: разброс между соседними вызовами больше разницы между режимами.
query_ms() {
  local q="$1" i t times=()
  for i in 1 2 3 4 5; do
    t="$( { TIMEFORMAT=%R; time innet -s --max-time 30 -G \
        "http://prometheus:9090/api/v1/query" --data-urlencode "query=$q" \
        >/dev/null 2>&1; } 2>&1 )"
    times+=("$t")
  done
  printf '%s\n' "${times[@]}" | sort -n | sed -n '3p' |
    awk '{ printf "%.0f", $1 * 1000 }'
}

# --- один прогон -------------------------------------------------------------

run_case() {
  local mode="$1" bomb="$2" label="$3"

  log "ПРОГОН $label: тома чистые, режим $mode"
  # ИМЕННО clean. Без аргумента down.sh только гасит контейнеры и СОХРАНЯЕТ
  # тома — это его штатное поведение, описанное в его же шапке.
  #
  # Первая редакция вызывала down.sh без аргумента, и замер «на чистых томах»
  # шёл на грязных: в Prometheus оставались серии предыдущего прогона, сумма
  # счётчика выходила ровно вдвое больше числа заказов. Поймал это не глаз, а
  # критерий «сумма = числу заказов» — то самое точное равенство, которое
  # кажется избыточным, пока не срабатывает.
  ./scripts/down.sh clean >/dev/null 2>&1 || true

  # Образ пересобирается, потому что режим взрыва живёт в КОДЕ приложения.
  # `docker compose up` собирает образ только если его нет; после правки
  # исходников он молча поднимет старый бинарник, и опыт не состоится.
  docker compose build go-frontend >/dev/null 2>&1

  if [ "$bomb" = "0" ]; then
    ./scripts/up.sh all otel > "$STAND_DIR/results/.last-up-card.log" 2>&1
  else
    export CARDINALITY_BOMB_MAX="$bomb"
    ./scripts/up.sh all "$mode" > "$STAND_DIR/results/.last-up-card.log" 2>&1
    unset CARDINALITY_BOMB_MAX
  fi

  # КОНТРОЛЬ: опыт обязан доказать, что он состоялся. Флаг внутри контейнера —
  # единственный способ отличить «взрыв выключен» от «взрыв не применился».
  local in_container
  in_container="$(docker exec ops-go-frontend env 2>/dev/null | grep '^CARDINALITY_BOMB_MAX=' | cut -d= -f2- || true)"
  echo "  CARDINALITY_BOMB_MAX в контейнере: '${in_container:-ПУСТО}' (ожидается '$bomb')"
  if [ "${in_container:-0}" != "$bomb" ]; then
    echo "ОШИБКА: режим не применился к приложению — прогон $label недостоверен." >&2
    exit 1
  fi

  docker compose run --rm --no-deps -T loadgen \
    -target http://go-frontend:8080 -requests "$REQUESTS" -seed "$SEED" \
    -concurrency 4 -json 2>/dev/null | sed -n '/^{/,$p' > "$LOAD_JSON" || true

  # Ждём, пока метрика доедет и перестанет расти. Здесь ожидаемое число серий
  # заранее неизвестно (в том и замер), поэтому ждём фиксированно, но щедро.
  sleep 30

  local series total_series orders_sum mem_prom mem_col qms created
  series="$(prom_scalar "count($METRIC)")"
  total_series="$(prom_scalar 'count({__name__=~".+"})')"
  # Сумма считается по сериям С ТЕМ НАБОРОМ ЛЕЙБЛОВ, который ожидается в этом
  # режиме: во взрыве — только серии с order_id, в остальных — только без него.
  # Иначе остаточные серии другого режима попадут в ту же сумму и удвоят её.
  if [ "$bomb" = "0" ] || [ "$mode" = "cardinality-cut" ]; then
    orders_sum="$(prom_scalar "sum($METRIC{order_id=\"\"})")"
  else
    orders_sum="$(prom_scalar "sum($METRIC{order_id=~\".+\"})")"
  fi
  mem_prom="$(mem_mib ops-prometheus)"
  mem_col="$(mem_mib ops-otelcol)"
  qms="$(query_ms "sum by (reserved) ($METRIC)")"
  created="$("$PYTHON_BIN" -c "
import sys, json
d = json.load(sys.stdin)
print(d['actual_by_status'].get('201', 0))
" < "$LOAD_JSON")"

  echo "  серий у $METRIC: $series"
  echo "  серий в Prometheus всего: $total_series"
  echo "  сумма счётчика: $orders_sum (заказов создано: $created)"
  echo "  память Prometheus: $mem_prom МиБ, Collector: $mem_col МиБ"
  echo "  запрос по метрике: $qms мс (медиана пяти)"

  # Результаты прогона возвращаются через файл: подоболочка не может изменить
  # переменные родителя, а вызывать функцию через $( ) нельзя — внутри печатается
  # ход прогона.
  printf '%s %s %s %s %s %s %s\n' "$series" "$total_series" "$orders_sum" \
    "$mem_prom" "$mem_col" "$qms" "$created" > "$STAND_DIR/results/.case-$label.txt"
}

# --- три прогона -------------------------------------------------------------

run_case otel 0 base
run_case cardinality "$BOMB_MAX" bomb
run_case cardinality-cut "$BOMB_MAX" cut

read -r S_BASE T_BASE SUM_BASE MP_BASE MC_BASE Q_BASE C_BASE < "$STAND_DIR/results/.case-base.txt"
read -r S_BOMB T_BOMB SUM_BOMB MP_BOMB MC_BOMB Q_BOMB C_BOMB < "$STAND_DIR/results/.case-bomb.txt"
read -r S_CUT  T_CUT  SUM_CUT  MP_CUT  MC_CUT  Q_CUT  C_CUT  < "$STAND_DIR/results/.case-cut.txt"

# --- вердикт -----------------------------------------------------------------

FAILED=0
verdict() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    printf "  ГОДЕН   %-46s %s (ожидалось %s)\n" "$name" "$got" "$want"
  else
    printf "  ОТКАЗ   %-46s %s (ожидалось %s)\n" "$name" "$got" "$want"
    FAILED=1
  fi
}

verdict_between() {
  local name="$1" got="$2" lo="$3" hi="$4"
  if "$PYTHON_BIN" -c "import sys; sys.exit(0 if $lo <= $got <= $hi else 1)" 2>/dev/null; then
    printf "  ГОДЕН   %-46s %s (ожидалось от %s до %s)\n" "$name" "$got" "$lo" "$hi"
  else
    printf "  ОТКАЗ   %-46s %s (ожидалось от %s до %s)\n" "$name" "$got" "$lo" "$hi"
    FAILED=1
  fi
}

echo
log "вердикт"
# База: ровно две серии — reserved принимает два значения, других лейблов нет.
verdict "base: серий у счётчика"          "$S_BASE" "2"
# Взрыв: точного равенства нет и быть не может. Серия — это КОМБИНАЦИЯ лейблов,
# поэтому один и тот же order_id, встретившийся и с reserved=true, и с false,
# даёт две серии. Отсюда границы: не меньше предела и не больше удвоенного.
verdict_between "bomb: серий у счётчика" "$S_BOMB" "$BOMB_MAX" "$((BOMB_MAX * 2))"
# Резка возвращает ровно базовое число серий.
verdict "cut: серий у счётчика"           "$S_CUT" "2"
# И — ключевой критерий, который ловит НАИВНУЮ резку. Удаление лейбла без
# агрегации схлопывает серии и одновременно теряет значения: счётчик
# показывает единицы вместо сотен. Серий при этом ровно 2, то есть по одному
# только счёту серий такая резка выглядит успешной.
verdict "cut: сумма счётчика = числу заказов" "$SUM_CUT" "$C_CUT"
verdict "base: сумма счётчика = числу заказов" "$SUM_BASE" "$C_BASE"

echo
if [ "$FAILED" = 0 ]; then
  echo "ИТОГ: взрыв воспроизводится, резка возвращает число серий И сохраняет значения."
else
  echo "ИТОГ: есть расхождения — смотри строки ОТКАЗ выше."
fi

# --- отчёт -------------------------------------------------------------------

MULT="$("$PYTHON_BIN" -c "print(round($S_BOMB / max($S_BASE, 1)))")"

mkdir -p "$STAND_DIR/results"
{
  echo "Кардинальность метрик: взрыв и цена резки"
  echo "=========================================="
  echo "Нагрузка: $REQUESTS запросов, seed $SEED. Предел уникальных order_id: $BOMB_MAX."
  echo "Три прогона на ЧИСТЫХ томах, различаются ровно одним условием."
  echo
  printf "%-6s %8s %10s %12s %10s %10s %8s\n" \
    "ПРОГОН" "СЕРИЙ" "ВСЕГО" "СУММА" "PROM,МиБ" "COL,МиБ" "ЗАПРОС"
  printf "%-6s %8s %10s %12s %10s %10s %8s\n" \
    "base" "$S_BASE" "$T_BASE" "$SUM_BASE" "$MP_BASE" "$MC_BASE" "${Q_BASE}мс"
  printf "%-6s %8s %10s %12s %10s %10s %8s\n" \
    "bomb" "$S_BOMB" "$T_BOMB" "$SUM_BOMB" "$MP_BOMB" "$MC_BOMB" "${Q_BOMB}мс"
  printf "%-6s %8s %10s %12s %10s %10s %8s\n" \
    "cut" "$S_CUT" "$T_CUT" "$SUM_CUT" "$MP_CUT" "$MC_CUT" "${Q_CUT}мс"
  echo
  echo "ГЛАВНОЕ ЧИСЛО: один лейбл увеличил количество серий у ОДНОЙ метрики"
  echo "в $MULT раз ($S_BASE -> $S_BOMB). Это не сложение, а умножение: каждое"
  echo "новое значение лейбла умножается на все комбинации остальных."
  echo
  echo "Почему серий не ровно $BOMB_MAX: серия — это комбинация ВСЕХ лейблов."
  echo "Один и тот же order_id, встретившийся и с reserved=true, и с false,"
  echo "даёт две серии. Отсюда $S_BOMB при пределе $BOMB_MAX."
  echo
  echo "ЧТО ЗДЕСЬ ДОКАЗАТЕЛЬНО, А ЧТО НЕТ"
  echo "Доказательно: число серий. Оно воспроизводится и объясняется арифметикой."
  echo "Память и время запроса на стендовом объёме — скорее шум: серий тысячи,"
  echo "а не миллионы, и разница между прогонами сопоставима с разбросом между"
  echo "повторами одного прогона. Приводятся для полноты, вывода на них строить"
  echo "нельзя. В проде цена кардинальности проявляется не на этом масштабе."
  echo
  echo "ЛОВУШКА РЕЗКИ (проверено на этом стенде)"
  echo "Наивная резка (transform + delete_key) даёт 2 серии из $S_BOMB — и"
  echo "значение счётчика 1 вместо сотен: точки с одинаковыми лейблами не"
  echo "складываются, экспортёр берёт последнюю. По одному счёту серий такая"
  echo "резка выглядит успешной. Правильно — metrics_transform с"
  echo "aggregate_labels, тогда значения сохраняются (см. критерий"
  echo "'cut: сумма счётчика = числу заказов')."
  echo
  if [ "$FAILED" = 0 ]; then echo "ИТОГ: ГОДЕН"; else echo "ИТОГ: ОТКАЗ"; fi
} > "$RESULT_FILE"
echo "Отчёт: results/$(basename "$RESULT_FILE")"

exit "$FAILED"
