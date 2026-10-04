#!/usr/bin/env bash
# tuning-demo.sh — оркестрирует часть стенда #6 ("эксплуатация"): тюнинг
# producer (batch.size/linger.ms/compression.type) и consumer
# (fetch.min.bytes/max.poll.records) через ../go/ops или ../java/ops, плюс
# demo quotas (producer_byte_rate через kafka-configs.sh --entity-type
# clients — чистая брокерная конфигурация, без клиентского кода).
#
# Все throughput-числа — ХАРАКТЕРНЫЙ прогон (host-зависимый: один docker
# demo-кластер на одной машине, без сетевой задержки между "разными"
# брокерами) — важен ОТНОСИТЕЛЬНЫЙ порядок между конфигурациями, не
# абсолютные msg/s (тот же принцип, что в стенде #3 acks-bench и стенде #4
# компрессии).
#
# Запуск: bash ops/tuning-demo.sh [-client=go|java] <scenario>
#   scenario: producer | consumer | quota | all (по умолчанию)

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # kafka/
GOBIN="go/bin/ops"
JAVA_JAR="java/ops/target/ops.jar"
CLIENT="${CLIENT:-go}"

require_binary() {
  case "$CLIENT" in
    go)
      if [ ! -x "$GOBIN" ]; then
        echo "[ops] $GOBIN не найден — собираю..."
        docker run --rm -v "$(pwd)/go:/app" -w /app golang:1.25 sh -c "go build -o bin/ops ./ops"
      fi
      ;;
    java)
      if [ ! -f "$JAVA_JAR" ]; then
        echo "[ops] $JAVA_JAR не найден — собираю..."
        docker run --rm -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
          sh -c "mvn -q -pl ops -am package -DskipTests"
      fi
      ;;
  esac
}

client_run() {
  case "$CLIENT" in
    java)
      local jargs=() a
      for a in "$@"; do
        case "$a" in
          -*) jargs+=("-$a") ;;
          *)  jargs+=("$a") ;;
        esac
      done
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
        java -jar /app/ops/target/ops.jar "${jargs[@]}" 2>&1 | { grep -v "^WARNING" || true; }
      ;;
    *)
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/ops "$@"
      ;;
  esac
}

# durflag KEY GOVAL JAVAMS — печатает "-KEY=GOVAL" (Go) или "--KEY-ms=JAVAMS" (Java),
# т.к. Go парсит длительности как time.Duration-строки ("30s"), а этот Java-стенд
# принимает простые миллисекунды (см. content-note в README про разные единицы).
durflag() {
  local key="$1" goval="$2" javams="$3"
  case "$CLIENT" in
    java) echo "-${key}-ms=${javams}" ;;
    *) echo "-${key}=${goval}" ;;
  esac
}

scenario_producer() {
  echo "=================================================================="
  echo "=== тюнинг producer: batch.size / linger.ms / compression.type ==="
  echo "=================================================================="
  local topic="demo-ops-tuning-producer" n=5000 vbytes=500

  echo "--- (1) baseline: batch=16384 linger=0 compression=none ---"
  client_run -scenario=tuning-producer -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 \
    -n=$n -value-bytes=$vbytes -batch-bytes=16384 -linger-ms=0 -compression=none

  echo "--- (2) больше batch + linger: batch=65536 linger=20 compression=none ---"
  client_run -scenario=tuning-producer -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 \
    -n=$n -value-bytes=$vbytes -batch-bytes=65536 -linger-ms=20 -compression=none

  echo "--- (3) то же + компрессия lz4: batch=65536 linger=20 compression=lz4 ---"
  client_run -scenario=tuning-producer -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 \
    -n=$n -value-bytes=$vbytes -batch-bytes=65536 -linger-ms=20 -compression=lz4

  echo "--- (4) маленький batch=2048 linger=0 (franz-go минимум ProducerBatchMaxBytes — 512, проверено живьём:"
  echo "    batch-bytes=1 падает при создании клиента 'max record batch bytes 1 is less than allowed 512'; а"
  echo "    batch-bytes=512 с value-bytes=500 создаёт клиента, но КАЖДАЯ запись падает — 500 байт значения +"
  echo "    overhead батча/записи Kafka (~60+ байт) не помещаются в 512-байтовый батч ни при каком batching."
  echo "    2048 — маленький, но реально рабочий размер для value-bytes=$vbytes.) ---"
  client_run -scenario=tuning-producer -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 \
    -n=$n -value-bytes=$vbytes -batch-bytes=2048 -linger-ms=0 -compression=none

  echo "[content-note] характерный прогон — абсолютные msg/s host-зависимы; смотрим на ОТНОСИТЕЛЬНЫЙ порядок между (1)-(4)"
}

scenario_consumer() {
  echo "=================================================================="
  echo "=== тюнинг consumer: fetch.min.bytes / fetch.max.wait.ms / max.poll.records ==="
  echo "=================================================================="
  local topic="demo-ops-tuning-consumer" n=20000 vbytes=500

  echo "--- сею $topic один раз ($n записей), дальше читаем ОДИН И ТОТ ЖЕ датасет разными consumer group ---"
  client_run -scenario=seed -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 -n=$n -value-bytes=$vbytes

  echo "--- (1) дефолт-подобный: fetch-min-bytes=1 fetch-max-wait=500ms max-poll-records=500 ---"
  client_run -scenario=tuning-consumer -topic="$topic" -group="tune-c1-$RANDOM" \
    -fetch-min-bytes=1 -fetch-max-wait-ms=500 -max-poll-records=500 $(durflag idle 10s 10000)

  echo "--- (2) маленькие max-poll-records=50 (больше вызовов poll на тот же объём) ---"
  client_run -scenario=tuning-consumer -topic="$topic" -group="tune-c2-$RANDOM" \
    -fetch-min-bytes=1 -fetch-max-wait-ms=500 -max-poll-records=50 $(durflag idle 10s 10000)

  echo "--- (3) высокий fetch-min-bytes=65536 (брокер ждёт накопления, крупнее фетч-пачки) ---"
  client_run -scenario=tuning-consumer -topic="$topic" -group="tune-c3-$RANDOM" \
    -fetch-min-bytes=65536 -fetch-max-wait-ms=500 -max-poll-records=500 $(durflag idle 10s 10000)

  echo "[content-note] характерный прогон — абсолютные msg/s host-зависимы; смотрим на avg-records/poll и относительный порядок"
}

scenario_quota() {
  echo "=================================================================="
  echo "=== quotas: producer_byte_rate через kafka-configs.sh (entity-type=clients) ==="
  echo "=================================================================="
  local topic="demo-ops-quota" client_id="quota-demo-client" vbytes=1000
  # ⚠️ n=8000 * 1000 байт = 8MB — НАМЕРЕННО больше "бесплатного" burst-кредита
  # квоты (producer_byte_rate * quota.window.size.seconds(1) * quota.window.num(11)
  # ≈ 100000*11 ≈ 1.1MB дефолтных окон). Живьём проверено:
  # при n=2000 (~2MB, чуть выше номинального окна, но ещё в пределах инерции
  # sensor'а) throttling НЕ проявлялся вовсе (клиент проскакивал почти без
  # задержки) — ни на franz-go, ни на официальном kafka-producer-perf-test.sh
  # (там же видно: первые ~3400 записей быстро, throttling включается ПОСЛЕ).
  # Поэтому объём здесь выбран заведомо больше порога, чтобы throttling был
  # гарантированно виден в одном прогоне, а не "может быть, а может и нет".
  local n=8000

  echo "--- (1) БЕЗ quota: n=$n * ${vbytes}b = $((n*vbytes/1000000))MB, client-id=$client_id ---"
  client_run -scenario=tuning-producer -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 \
    -n=$n -value-bytes=$vbytes -batch-bytes=16384 -linger-ms=10 -compression=none -client-id="$client_id" -label="БЕЗ-quota"

  echo "--- применяю quota: producer_byte_rate=100000 B/s (~97.7 КБ/с) для client-id=$client_id ---"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
    --entity-type clients --entity-name "$client_id" --alter --add-config producer_byte_rate=100000 2>&1
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
    --entity-type clients --entity-name "$client_id" --describe 2>&1

  echo "--- (2) С quota — тот же клиент, тот же объём ---"
  client_run -scenario=tuning-producer -topic="$topic" -recreate=true -partitions=3 -rf=3 -minisr=1 \
    -n=$n -value-bytes=$vbytes -batch-bytes=16384 -linger-ms=10 -compression=none -client-id="$client_id" -label="С-quota"

  echo "--- убираю quota (cleanup) ---"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
    --entity-type clients --entity-name "$client_id" --alter --delete-config producer_byte_rate 2>&1

  echo "[content-note] ожидаем: throughput (2) на порядок НИЖЕ (1) и приближается к ~100000 B/s (producer_byte_rate) — burst-кредит окна квоты покрывает первые ~1.1МБ почти без задержки, дальше включается throttling."
}

main() {
  while [ $# -gt 0 ]; do
    case "$1" in
      -client=*|--client=*) CLIENT="${1#*=}"; shift ;;
      -client|--client) CLIENT="$2"; shift 2 ;;
      *) break ;;
    esac
  done
  case "$CLIENT" in
    go|java) ;;
    *) echo "неизвестный -client=$CLIENT (go|java)" >&2; exit 1 ;;
  esac
  echo "[ops] клиент: $CLIENT"

  require_binary
  local scenario="${1:-all}"
  case "$scenario" in
    producer) scenario_producer ;;
    consumer) scenario_consumer ;;
    quota) scenario_quota ;;
    all)
      scenario_producer
      echo
      scenario_consumer
      echo
      scenario_quota
      ;;
    *)
      echo "неизвестный сценарий: $scenario (producer|consumer|quota|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО: tuning-demo завершён (client=$CLIENT) ==="
}

main "$@"
