#!/usr/bin/env bash
# Замер 4: что происходит при перезапуске конвейера.
# Использование: ./scripts/restart-pipeline.sh <vector|otelcol|fluentbit> <keep|wipe>
#
#   keep — состояние (чекпойнты чтения файлов) сохраняется: проверяем, что
#          конвейер не перечитывает уже обработанное;
#   wipe — состояние удаляется: проверяем цену потери чекпойнтов.
#
# Оба прогона нужны именно парой. Один только keep показал бы «всё хорошо»,
# один только wipe — «инструмент дублирует события»; смысл в разнице.
set -euo pipefail

PIPELINE="${1:-vector}"
MODE="${2:-keep}"
RATE=2000
DURATION=20

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1
AUTH="admin:VectorDemo#2026"

case "$PIPELINE" in
  vector)    INDEX=logs-vector;    STATE_VOLUME=vector-vs-collector_vector-data ;;
  otelcol)   INDEX=logs-otelcol;   STATE_VOLUME=vector-vs-collector_otelcol-data ;;
  fluentbit) INDEX=logs-fluentbit; STATE_VOLUME=vector-vs-collector_fluentbit-data ;;
  *) echo "конвейер: vector | otelcol | fluentbit" >&2; exit 2 ;;
esac
case "$MODE" in keep|wipe) ;; *) echo "режим: keep | wipe" >&2; exit 2 ;; esac

OUT="results/06-restart-${PIPELINE}-${MODE}.txt"

count_now() {
  docker compose exec -T opensearch curl -sk --connect-timeout 3 --max-time 10 \
    -u "$AUTH" "https://localhost:9200/${INDEX}/_count" 2>/dev/null \
    | sed 's/.*"count":\([0-9]*\).*/\1/'
}

# Ждёт, пока счётчик перестанет расти. Возвращает финальное значение.
#
# Критерий строгий намеренно: ПЯТЬ одинаковых показаний подряд с интервалом
# 4 с, то есть 20 секунд тишины. Первая версия требовала трёх показаний с
# интервалом 3 с (9 секунд), и этого не хватило: Vector доставляет пачками,
# и пауза между ними превысила окно. Замер зафиксировал «до перезапуска»
# 18 962 вместо 36 627 и посчитал последующую ДОдоставку дублями — то есть
# приписал инструменту дефект, которого нет.
wait_stable() {
  local prev=-1 stable=0 cur=""
  for _ in $(seq 1 60); do
    cur="$(count_now)"
    if [[ "$cur" =~ ^[0-9]+$ ]]; then
      if [[ "$cur" == "$prev" ]]; then
        stable=$((stable+1)); (( stable >= 5 )) && break
      else
        stable=0
      fi
      prev="$cur"
    fi
    sleep 4
  done
  printf '%s' "$cur"
}

# ── Подготовка (порядок как в bench.sh) ─────────────────────────────────
docker compose rm -sf "$PIPELINE" >/dev/null 2>&1 || true
docker volume rm "$STATE_VOLUME" >/dev/null 2>&1 || true
docker compose up -d opensearch prometheus >/dev/null
docker compose run --rm --entrypoint sh loadgen -c 'rm -f /logs/*.log /logs/manifest.json' >/dev/null 2>&1 || true
docker compose exec -T opensearch curl -sk --connect-timeout 5 --max-time 20 \
  -u "$AUTH" -X DELETE "https://localhost:9200/${INDEX}" >/dev/null 2>&1 || true
./scripts/up.sh "$PIPELINE" >/dev/null

docker compose run --rm loadgen -rate "$RATE" -duration "${DURATION}s" >/dev/null 2>&1
before="$(wait_stable)"

{
  echo "Замер 4: перезапуск конвейера $PIPELINE, режим $MODE"
  echo "Дата: $(docker compose exec -T opensearch date -u '+%Y-%m-%d %H:%M:%S UTC' 2>/dev/null | tr -d '\r')"
  echo "Параметры: rate=$RATE, длительность=${DURATION}s"
  echo
  echo "в индексе ДО перезапуска: $before"
} > "$OUT"

if [[ "$MODE" == "keep" ]]; then
  docker compose restart "$PIPELINE" >/dev/null
  echo "перезапуск: docker compose restart (том состояния СОХРАНЁН)" >> "$OUT"
else
  # Именно rm -sf, а не stop: `docker volume rm` на томе остановленного, но
  # существующего контейнера молча не срабатывает, и «очистка состояния»
  # оказалась бы фикцией — конвейер продолжил бы с прежних чекпойнтов.
  docker compose rm -sf "$PIPELINE" >/dev/null 2>&1 || true
  docker volume rm "$STATE_VOLUME" >/dev/null 2>&1 || true
  ./scripts/up.sh "$PIPELINE" >/dev/null
  echo "перезапуск: контейнер пересоздан, том состояния УДАЛЁН" >> "$OUT"
fi

sleep 40
after="$(wait_stable)"

{
  echo "в индексе ПОСЛЕ перезапуска: $after"
  echo "дельта: $(( after - before ))"
  echo
  echo "--- лог конвейера после перезапуска (последние 10 строк) ---"
  docker compose logs --tail 10 "$PIPELINE" 2>&1 | sed 's/^/  /'
} >> "$OUT"

tail -8 "$OUT"
