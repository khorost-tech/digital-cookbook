#!/usr/bin/env bash
# Замер 5: что происходит с потоком, когда синк недоступен.
# Использование: ./scripts/fail-sink.sh <block|drop_newest>
#
# Параметры нагрузки переопределяются через GEN_RATE/GEN_DURATION (по умолчанию
# ниже) — дефолт из брифа (rate 5000, duration 60s) даёт ~45 МиБ полезной
# нагрузки в буфере, а минимальный дисковый буфер Vector — 256 МиБ. Такой прогон
# буфер не переполнил бы и показал бы "потерь нет" как артефакт маленькой
# нагрузки, а не свойство режима. Значения по умолчанию ниже подобраны так,
# чтобы буфер гарантированно переполнялся с запасом (см. results/05-fail-sink-*.txt
# и task-10-report.md — там же оценка байт/событие и расчёт).
set -euo pipefail

MODE="${1:-block}"
case "$MODE" in
  block)       AGG_CONFIG="/etc/vector/aggregator-kafka.yaml" ;;
  drop_newest) AGG_CONFIG="/etc/vector/variants/agg-drop-newest.yaml" ;;
  *) echo "режим должен быть block или drop_newest" >&2; exit 2 ;;
esac

GEN_RATE="${GEN_RATE:-8000}"
GEN_DURATION="${GEN_DURATION:-240s}"

# Имя файла результата — с дефисом (drop-newest), а не с подчёркиванием, как
# аргумент CLI (drop_newest): так зафиксировано в интерфейсах задачи.
case "$MODE" in
  block)       OUT_NAME="block" ;;
  drop_newest) OUT_NAME="drop-newest" ;;
esac

cd "$(dirname "$0")/.."
OUT="results/05-fail-sink-${OUT_NAME}.txt"

# down.sh должен реально погасить стенд перед новым прогоном — `docker compose
# down -v` идемпотентен и не падает на пустом/отсутствующем стенде, поэтому
# скрывать его код возврата незачем. Если он всё же падает (например, том
# занят), продолжать поверх недогашенного стенда нельзя: второй режим тогда
# посчитает остатки первого, и оба числа "потерь" станут враньём без единого
# предупреждения.
if ! ./scripts/down.sh; then
  echo "не удалось погасить стенд перед прогоном — замер не начат, чтобы не считать остатки прошлого запуска" >&2
  exit 1
fi
AGG_CONFIG="$AGG_CONFIG" ./scripts/up.sh kafka

{
  echo "=== режим: $MODE"
  echo "=== AGG_CONFIG: $AGG_CONFIG"
  echo "=== параметры генерации: rate=$GEN_RATE duration=$GEN_DURATION"
} | tee "$OUT"

echo "=== останавливаем OpenSearch" | tee -a "$OUT"
docker compose stop opensearch

echo "=== генерация при недоступном синке" | tee -a "$OUT"
docker compose run --rm loadgen -rate "$GEN_RATE" -duration "$GEN_DURATION"

# du сразу после окончания генерации занижает размер буфера: агрегатор ещё
# докачивает из Kafka то, что генератор успел произвести за 240с, и буфер
# продолжает расти какое-то время уже ПОСЛЕ остановки генератора (проверено
# живьём: сразу после генерации — 131092 КиБ, а не плато у потолка 262144 КиБ).
# Ждём, пока размер перестанет расти (3 одинаковых замера подряд), прежде чем
# считать буфер измеренным и поднимать OpenSearch.
FILL_ATTEMPTS="${FILL_ATTEMPTS:-40}"
prev_buf=-1
buf_stable=0
buf_size=0
for _ in $(seq 1 "$FILL_ATTEMPTS"); do
  cur_buf="$(docker compose exec -T aggregator \
    sh -c 'du -sk /var/lib/vector 2>/dev/null | cut -f1' | tr -d ' \r')"
  buf_size="$cur_buf"
  if [[ "$cur_buf" == "$prev_buf" ]]; then
    buf_stable=$((buf_stable + 1))
    if (( buf_stable >= 3 )); then break; fi
  else
    buf_stable=0
  fi
  prev_buf="$cur_buf"
  sleep 5
done
if (( buf_stable < 3 )); then
  echo "буфер не стабилизировался за $FILL_ATTEMPTS попыток (последнее значение: $buf_size КиБ, всё ещё растёт) — замер буфера недостоверен, увеличь FILL_ATTEMPTS" >&2
  exit 1
fi
echo "буфер на диске, КиБ: $buf_size" | tee -a "$OUT"

echo "=== поднимаем OpenSearch и ждём слива буфера" | tee -a "$OUT"
docker compose start opensearch

# Дренаж буфера может занять несколько минут (буфер намеренно переполняется,
# см. заголовок файла) — фиксированный sleep здесь недостаточен и переменчив
# по железу. Вместо этого ждём, пока счётчик _count перестанет расти: та же
# логика стабилизации, что и в scripts/verify.sh, но с более широким окном
# ожидания (до 20 минут) — при бОльшем объёме буфера расширь ATTEMPTS.
ATTEMPTS="${DRAIN_ATTEMPTS:-120}"
prev_idx=-1
idx_stable=0
indexed=0
idx_read_ok=0
for _ in $(seq 1 "$ATTEMPTS"); do
  if ! cur_idx="$(curl -sk --connect-timeout 3 --max-time 5 -u "admin:VectorDemo#2026" \
      "https://localhost:9224/logs-app-*/_count" | sed 's/.*"count":\([0-9]*\).*/\1/')"; then
    sleep 10
    continue
  fi
  if ! [[ "$cur_idx" =~ ^[0-9]+$ ]]; then
    sleep 10
    continue
  fi
  idx_read_ok=1
  indexed="$cur_idx"
  if [[ "$indexed" == "$prev_idx" ]]; then
    idx_stable=$((idx_stable + 1))
    if (( idx_stable >= 3 )); then break; fi
  else
    idx_stable=0
  fi
  prev_idx="$indexed"
  sleep 10
done
if (( idx_read_ok == 0 )); then
  echo "не удалось прочитать _count индекса за отведённое окно" >&2
  exit 1
fi
# idx_read_ok==1 доказывает только то, что _count читался — не то, что он
# перестал расти. Без этой проверки скрипт при исчерпании ATTEMPTS молча
# зафиксировал бы недодренированное (заниженное) значение как финальное —
# то есть "потери на синке" оказались бы ложно завышены на ещё не доехавший
# хвост, и никакого предупреждения об этом не было бы.
if (( idx_stable < 3 )); then
  echo "индекс не стабилизировался за $ATTEMPTS попыток (последнее значение: $indexed, дренаж, похоже, не завершился) — замер недостоверен, увеличь DRAIN_ATTEMPTS" >&2
  exit 1
fi

# Независимая сверка через собственные внутренние метрики Vector (Task 9,
# port 9598 на хосте): buffer_received_events_total для buffer_id="opensearch"
# — сколько событий буфер синка реально ПРИНЯЛ (а не сколько было предложено
# from_kafka/mark_aggregator). Расхождение между component_received_events_total
# у mark_aggregator и buffer_received_events_total у opensearch — это именно
# отказ буфера принять событие (drop_newest), не сетевая ошибка и не что-то
# ещё. Совпадение с (ожидалось - в индексе) — доказательство, что подмена
# конфига реально сработала и различие двух режимов объясняется буфером.
# Молчаливая деградация здесь недопустима: если :9598/metrics недоступен или
# нужные поля не нашлись, замер лишается главного независимого подтверждения
# — это фатальная ошибка, а не "н/д" в файле результата.
if ! metrics="$(curl -sf --connect-timeout 3 --max-time 5 http://localhost:9598/metrics)"; then
  echo "не удалось прочитать внутренние метрики Vector (:9598/metrics) — без них потери на синке не подтверждены независимо" >&2
  exit 1
fi
# `|| true` здесь обязателен: под set -euo pipefail пустой grep (нет совпадения)
# сделал бы всю подстановку "неуспешной" и уронил бы скрипт ПРЯМО на этой
# строке, сырым сбоем без внятного сообщения — то есть проверку -z ниже
# скрипт до неё бы просто не дожил. Явную проверку пустого результата делает
# именно блок if [[ -z ... ]] ниже, а не факт падения/непадения этой команды.
mark_agg_received="$(printf '%s' "$metrics" | grep 'component_id="mark_aggregator"' | grep component_received_events_total | sed 's/.* \([0-9.]*\) [0-9]*$/\1/' || true)"
buf_opensearch_received="$(printf '%s' "$metrics" | grep 'buffer_id="opensearch"' | grep buffer_received_events_total | sed 's/.* \([0-9.]*\) [0-9]*$/\1/' || true)"
if [[ -z "$mark_agg_received" || -z "$buf_opensearch_received" ]]; then
  echo "внутренние метрики Vector прочитаны, но нужные поля (mark_aggregator/opensearch buffer_received_events_total) не нашлись — формат метрик изменился?" >&2
  exit 1
fi
{
  echo "=== внутренние метрики Vector (:9598/metrics)"
  echo "mark_aggregator component_received_events_total: $mark_agg_received"
  echo "opensearch buffer_received_events_total (принято буфером синка): $buf_opensearch_received"
} | tee -a "$OUT"

manifest="$(docker compose run --rm --entrypoint sh loadgen -c 'cat /logs/manifest.json' | tr -d '\n')"
field() { printf '%s' "$manifest" | sed "s/.*\"$1\":\([0-9]*\).*/\1/"; }
gen_total="$(field total)"
nginx_expected="$(field nginx_expected)"
app_expected="$(field app_expected)"
expected_no_sink_loss=$(( nginx_expected + app_expected ))

{
  echo "сгенерировано (сырых строк, nginx+app): $gen_total"
  echo "ожидалось без потерь синка (nginx_expected+app_expected после filter/dedupe): $expected_no_sink_loss"
  echo "в индексе:      $indexed"
  echo "разница от сырого total: $(( gen_total - indexed ))"
  echo "потери на синке (expected_no_sink_loss - indexed): $(( expected_no_sink_loss - indexed ))"
} | tee -a "$OUT"

echo "готово: $OUT"
