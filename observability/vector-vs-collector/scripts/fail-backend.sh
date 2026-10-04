#!/usr/bin/env bash
# Замер 3: что теряет конвейер, пока бэкенд недоступен.
# Использование: ./scripts/fail-backend.sh <vector|otelcol|fluentbit> <rate> <downtime-sec>
#
# Сценарий: поток идёт → через 15 с OpenSearch останавливается на downtime →
# поднимается обратно → ждём стабилизации счётчика → сверяем с ожиданием.
#
# Конвейеры берутся в вариантах *buffered*: у каждого своя модель дисковой
# устойчивости (буфер синка у Vector, очередь экспортёра у Collector,
# хранилище чанков входа у Fluent Bit), и замер сравнивает именно их.
set -euo pipefail

PIPELINE="${1:-vector}"
RATE="${2:-5000}"
DOWNTIME="${3:-60}"
# Режим отказа. Разница между ними принципиальная, а не косметическая:
#
#   stop  — контейнер останавливается, и docker убирает его DNS-запись.
#           Клиент получает «no such host». Так выглядит удалённый под.
#   pause — процессы замораживаются, но контейнер существует и DNS-запись
#           на месте. Клиент упирается в таймаут соединения. Так выглядит
#           зависший или перегруженный бэкенд — в проде это встречается чаще.
#
# Замер обоих режимов нужен потому, что Collector классифицирует DNS-ошибку
# как ПОСТОЯННУЮ и выбрасывает данные, не пытаясь повторить, тогда как
# отказ соединения для него ошибка временная. Мерить только stop значило бы
# выдать особенность docker-сети за свойство инструмента.
FAILMODE="${4:-stop}"
DURATION=60
case "$FAILMODE" in stop|pause) ;; *) echo "режим отказа: stop | pause" >&2; exit 2 ;; esac

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
AUTH="admin:VectorDemo#2026"

# Метка для имени файла берётся ДО того, как PIPELINE может быть переопределён
# ниже на имя сервиса compose: иначе прогон otelcol-bigqueue затёр бы результат
# обычного otelcol, и сравнивать было бы не с чем.
LABEL="$PIPELINE"

case "$PIPELINE" in
  vector)
    export VECTOR_CONFIG=/etc/vector/buffered.yaml
    INDEX=logs-vector-buffered
    STATE_VOLUME=vector-vs-collector_vector-data
    STATE_PATH=/var/lib/vector
    ;;
  otelcol)
    export OTELCOL_CONFIG=/etc/otelcol/buffered.yaml
    INDEX=logs-otelcol-buffered
    STATE_VOLUME=vector-vs-collector_otelcol-data
    STATE_PATH=/var/lib/otelcol
    ;;
  fluentbit)
    export FLUENTBIT_CONFIG=/etc/fluent-bit/buffered.yaml
    INDEX=logs-fluentbit-buffered
    STATE_VOLUME=vector-vs-collector_fluentbit-data
    STATE_PATH=/var/lib/fluent-bit
    ;;
  # Отдельная ветка, а не правка предыдущей: нужно СРАВНИТЬ поведение при
  # умолчании и при явно заданной ёмкости очереди, поэтому оба варианта
  # должны оставаться прогоняемыми.
  otelcol-bigqueue)
    export OTELCOL_CONFIG=/etc/otelcol/buffered-bigqueue.yaml
    INDEX=logs-otelcol-bigqueue
    STATE_VOLUME=vector-vs-collector_otelcol-data
    STATE_PATH=/var/lib/otelcol
    PIPELINE=otelcol   # имя сервиса compose то же
    ;;
  *) echo "конвейер: vector | otelcol | otelcol-bigqueue | fluentbit" >&2; exit 2 ;;
esac

OUT="results/05-fail-backend-${LABEL}-${FAILMODE}.txt"

# Бэкенд обязан подняться обратно, чем бы ни кончился скрипт. Без этого
# прерывание в середине опыта оставляет OpenSearch остановленным, и следующий
# замер начинается в сломанном окружении — а выглядит это как отказ уже
# другого конвейера. Проверено на себе: прогон otelcol прервался во время
# простоя и оставил стенд без бэкенда.
restore_backend() {
  docker compose unpause opensearch >/dev/null 2>&1 || true
  docker compose start opensearch >/dev/null 2>&1 || true
}
trap restore_backend EXIT INT TERM

# Размер состояния на диске меряем ИЗВНЕ, через busybox с примонтированным
# томом: образы Collector и Fluent Bit distroless, внутри нет ни du, ни sh.
state_size_kb() {
  docker run --rm -v "${STATE_VOLUME}:/data" busybox:1.37 du -sk /data 2>/dev/null | awk '{print $1}'
}

# ── Подготовка: тот же порядок, что в bench.sh, и по той же причине ──────
docker compose rm -sf "$PIPELINE" >/dev/null 2>&1 || true
docker volume rm "$STATE_VOLUME" >/dev/null 2>&1 || true
docker compose up -d opensearch prometheus >/dev/null
docker compose run --rm --entrypoint sh loadgen -c 'rm -f /logs/*.log /logs/manifest.json' >/dev/null 2>&1 || true
docker compose exec -T opensearch curl -sk --connect-timeout 5 --max-time 20 \
  -u "$AUTH" -X DELETE "https://localhost:9200/${INDEX}" >/dev/null 2>&1 || true
./scripts/up.sh "$PIPELINE" >/dev/null

# Конвейер обязан быть жив. Без этой проверки замер идёт дальше по мёртвому
# контейнеру и выдаёт «потеряно всё» как свойство инструмента, тогда как на
# деле процесс не стартовал. Живьём: Vector отверг размер дискового буфера
# и умер на старте, а скрипт спокойно досчитал до конца.
sleep 8
if [[ "$(docker compose ps --format '{{.State}}' "$PIPELINE" 2>/dev/null)" != "running" ]]; then
  echo "конвейер $PIPELINE не запустился — замер бессмыслен. Лог:" >&2
  docker compose logs --tail 20 "$PIPELINE" >&2
  exit 1
fi

{
  echo "Замер 3: отказ бэкенда, конвейер $LABEL, режим $FAILMODE"
  echo "Дата: $(docker compose exec -T opensearch date -u '+%Y-%m-%d %H:%M:%S UTC' 2>/dev/null | tr -d '\r')"
  echo "Параметры: rate=$RATE, длительность прогона=${DURATION}s, простой бэкенда=${DOWNTIME}s"
  echo "Конфиг: $(printenv VECTOR_CONFIG OTELCOL_CONFIG FLUENTBIT_CONFIG 2>/dev/null | head -1)"
  echo
} > "$OUT"

if ! docker compose run -d --rm loadgen -rate "$RATE" -duration "${DURATION}s" >/dev/null; then
  echo "не запустить генератор" >&2
  exit 1
fi

sleep 15
echo "состояние на диске ДО остановки бэкенда: $(state_size_kb) КиБ" >> "$OUT"

if [[ "$FAILMODE" == "stop" ]]; then
  echo "--- ОСТАНАВЛИВАЕМ OpenSearch на ${DOWNTIME}s (DNS-запись исчезает) ---" >> "$OUT"
  docker compose stop opensearch >/dev/null
else
  echo "--- ЗАМОРАЖИВАЕМ OpenSearch на ${DOWNTIME}s (DNS на месте, соединение виснет) ---" >> "$OUT"
  docker compose pause opensearch >/dev/null
fi
sleep "$DOWNTIME"

size_during="$(state_size_kb)"
echo "состояние на диске во время недоступности: ${size_during} КиБ" >> "$OUT"
{
  echo "--- лог конвейера во время недоступности (последние 15 строк) ---"
  docker compose logs --tail 15 "$PIPELINE" 2>&1 | sed 's/^/  /'
  echo
} >> "$OUT"

if [[ "$FAILMODE" == "stop" ]]; then
  docker compose start opensearch >/dev/null
else
  docker compose unpause opensearch >/dev/null
fi
echo "--- OpenSearch возвращён в работу ---" >> "$OUT"

# Ждём готовности бэкенда, затем стабилизации счётчика.
for _ in $(seq 1 40); do
  if docker compose exec -T opensearch curl -sk --connect-timeout 3 --max-time 8 \
      -u "$AUTH" "https://localhost:9200/_cluster/health" 2>/dev/null | grep -q '"status"'; then
    break
  fi
  sleep 3
done

if ! manifest="$(docker compose run --rm --entrypoint sh loadgen -c 'cat /logs/manifest.json')"; then
  echo "не прочитать манифест генератора" >&2
  exit 1
fi
mfield() { printf '%s' "$manifest" | tr -d '\r\n' | sed "s/.*\"$1\":\([0-9]*\).*/\1/"; }
gen_total="$(mfield total)"; gen_app="$(mfield app)"; nginx_expected="$(mfield nginx_expected)"
for v in "$gen_total" "$gen_app" "$nginx_expected"; do
  [[ "$v" =~ ^[0-9]+$ ]] || { echo "манифест генератора неполон: $manifest" >&2; exit 1; }
done
expected=$(( nginx_expected + gen_app ))

prev=-1; stable=0; indexed=""; read_ok=0
for _ in $(seq 1 60); do
  cur="$(docker compose exec -T opensearch curl -sk --connect-timeout 3 --max-time 10 \
        -u "$AUTH" "https://localhost:9200/${INDEX}/_count" 2>/dev/null \
        | sed 's/.*"count":\([0-9]*\).*/\1/')"
  if [[ "$cur" =~ ^[0-9]+$ ]]; then
    read_ok=1; indexed="$cur"
    if [[ "$cur" == "$prev" ]]; then
      stable=$((stable+1)); (( stable >= 3 )) && break
    else
      stable=0
    fi
    prev="$cur"
  fi
  sleep 3
done
(( read_ok )) || { echo "не прочитать _count индекса $INDEX" >&2; exit 1; }

{
  echo "сгенерировано:   $gen_total"
  echo "ожидалось:       $expected"
  echo "в индексе:       $indexed"
  echo "потеряно:        $(( expected - indexed ))"
  echo "состояние на диске после доставки: $(state_size_kb) КиБ"
  echo
  echo "--- лог конвейера после восстановления (последние 10 строк) ---"
  docker compose logs --tail 10 "$PIPELINE" 2>&1 | sed 's/^/  /'
} >> "$OUT"

tail -12 "$OUT"
