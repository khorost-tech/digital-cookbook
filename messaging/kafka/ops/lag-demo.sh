#!/usr/bin/env bash
# lag-demo.sh — оркестрирует часть стенда #6 ("эксплуатация"), которая
# демонстрирует consumer lag живьём: медленный консьюмер отстаёт от
# продолжающего писать продюсера (лаг РАСТЁТ), затем консьюмер переходит в
# быстрый режим и вычитывает backlog (лаг ПАДАЕТ к нулю) — параллельно хост
# поллит `kafka-consumer-groups.sh --describe`, колонка LAG. Отдельный
# под-сценарий kill'ит одну реплику (НЕ лидера) демо-топика и показывает
# under-replicated-partitions (переиспользует механику broker-kill.sh).
#
# Продюсер/консьюмер — Go (franz-go, ../go/ops) или Java (kafka-clients,
# ../java/ops), запускаются ФОНОВЫМИ контейнерами (`docker run -d`), пока
# ХОСТ поллит `kafka-consumer-groups.sh --describe` — у клиента внутри
# контейнера нет доступа к docker socket, поэтому именно хост синхронизирует
# запуск/остановку с моментами наблюдения (тот же принцип, что в
# broker-kill.sh/eos-kill.sh).
#
# Запуск: bash ops/lag-demo.sh [-client=go|java] <scenario>
#   scenario: growth-shrink | under-replicated | all (по умолчанию)

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # kafka/
COMPOSE="compose/compose.yml"
GOBIN="go/bin/ops"
JAVA_JAR="java/ops/target/ops.jar"
CLIENT="${CLIENT:-go}"

TOPIC="demo-ops-lag"
GROUP="ops-lag-group"

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

# run_bg NAME ARGS... — запускает выбранный клиент фоновым именованным
# контейнером с данными -scenario/... аргументами в СВОЁМ (Go или Java)
# формате флагов — см. content-note в README про разные единицы измерения
# длительности (Go: -duration=45s парсится как time.Duration; Java:
# --duration-ms=45000 — простое число мс). Тела сценариев ниже поэтому не
# универсальны буква-в-букву между клиентами, а вызывают per-client хелперы
# go_args_lag/java_args_lag ниже.
run_bg() {
  local name="$1"; shift
  docker rm -f "$name" >/dev/null 2>&1 || true
  case "$CLIENT" in
    java)
      docker run -d --name "$name" --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
        java -jar /app/ops/target/ops.jar "$@" >/dev/null
      ;;
    *)
      docker run -d --name "$name" --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/ops "$@" >/dev/null
      ;;
  esac
}

wait_log_marker() {
  local name="$1" marker="$2" timeout="${3:-20}"
  for i in $(seq 1 "$timeout"); do
    if docker logs "$name" 2>&1 | grep -q "$marker"; then
      return 0
    fi
    sleep 1
  done
  echo "[ops] ПРЕДУПРЕЖДЕНИЕ: маркер '$marker' не появился в логах $name за ${timeout}с"
  return 1
}

is_running() {
  [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null || echo false)" = "true" ]
}

wait_healthy() {
  local name="$1"
  echo "[ops] жду healthy: $name"
  for i in $(seq 1 20); do
    status=$(docker inspect --format '{{.State.Health.Status}}' "$name" 2>/dev/null || echo "unknown")
    if [ "$status" = "healthy" ]; then
      echo "[ops] $name healthy"
      return 0
    fi
    sleep 3
  done
  echo "[ops] ПРЕДУПРЕЖДЕНИЕ: $name не стал healthy за 60с (status=$status)"
}

describe_group() {
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-consumer-groups.sh \
    --bootstrap-server localhost:9092 --describe --group "$GROUP" 2>&1
}

sum_lag() {
  # kafka-consumer-groups.sh --describe печатает заголовок:
  # GROUP TOPIC PARTITION CURRENT-OFFSET LOG-END-OFFSET LAG CONSUMER-ID HOST CLIENT-ID
  # LAG — колонка 6. Определяем её позицию по заголовку динамически (на
  # случай будущей смены формата), суммируем числовые значения по строкам
  # данных.
  echo "$1" | awk '
    $1=="GROUP" { for (i=1;i<=NF;i++) if ($i=="LAG") lagcol=i; next }
    lagcol && $lagcol ~ /^[0-9]+$/ { s+=$lagcol }
    END { print s+0 }
  '
}

# --- Сценарий 1: лаг растёт (медленный консьюмер отстаёт от продюсера), затем падает к 0 ---
scenario_growth_shrink() {
  echo "=================================================================="
  echo "=== consumer lag: рост при отставании, падение к 0 при догоне ==="
  echo "=================================================================="

  local prod_duration_s=40 prod_rate=20 cons_slow_count=300 cons_slow_delay_ms=150 cons_run_for_s=90 cons_idle_s=10

  echo "--- запускаю продюсер (фон): rate=${prod_rate}/s duration=${prod_duration_s}s (топик $TOPIC пересоздаётся) ---"
  case "$CLIENT" in
    java)
      run_bg ops-lag-producer --scenario=seed-continuous --topic="$TOPIC" --recreate=true --partitions=3 --rf=3 --minisr=2 \
        --duration-ms=$((prod_duration_s*1000)) --rate=$prod_rate --value-bytes=300
      ;;
    *)
      run_bg ops-lag-producer -scenario=seed-continuous -topic="$TOPIC" -recreate=true -partitions=3 -rf=3 -minisr=2 \
        -duration=${prod_duration_s}s -rate=$prod_rate -value-bytes=300
      ;;
  esac
  wait_log_marker ops-lag-producer "СТАРТ" 20

  echo "--- запускаю консьюмер (фон): slow-count=$cons_slow_count slow-delay=${cons_slow_delay_ms}ms run-for=${cons_run_for_s}s ---"
  case "$CLIENT" in
    java)
      run_bg ops-lag-consumer --scenario=lag-consume --topic="$TOPIC" --group="$GROUP" \
        --slow-count=$cons_slow_count --slow-delay-ms=$cons_slow_delay_ms \
        --run-for-ms=$((cons_run_for_s*1000)) --idle-ms=$((cons_idle_s*1000))
      ;;
    *)
      run_bg ops-lag-consumer -scenario=lag-consume -topic="$TOPIC" -group="$GROUP" \
        -slow-count=$cons_slow_count -slow-delay=${cons_slow_delay_ms}ms \
        -run-for=${cons_run_for_s}s -idle=${cons_idle_s}s
      ;;
  esac

  echo "--- поллю kafka-consumer-groups.sh --describe каждые 4с, пока консьюмер работает ---"
  local t0 max_lag_seen=0
  t0=$(date +%s)
  while is_running ops-lag-consumer; do
    sleep 4
    out=$(describe_group || true)
    lag=$(sum_lag "$out")
    now=$(date +%s)
    echo "[poll t=+$((now-t0))s] SUM(LAG)=$lag"
    if [ "$lag" -gt "$max_lag_seen" ]; then max_lag_seen=$lag; fi
  done

  echo "--- консьюмер завершился, финальная проверка (жду LAG=0, до 20с) ---"
  local final_lag=-1
  for i in $(seq 1 10); do
    out=$(describe_group || true)
    final_lag=$(sum_lag "$out")
    echo "[final-poll $i] SUM(LAG)=$final_lag"
    if [ "$final_lag" = "0" ]; then break; fi
    sleep 2
  done

  echo "--- реальный вывод продюсера ---"
  docker logs ops-lag-producer 2>&1 | tail -10
  echo "--- реальный вывод консьюмера ---"
  docker logs ops-lag-consumer 2>&1 | tail -10

  echo "--- MAX(SUM(LAG)) за прогон: $max_lag_seen; финальный SUM(LAG): $final_lag ---"
  if [ "$max_lag_seen" -le 0 ]; then
    echo "[assert] FAIL: лаг ни разу не был > 0 — консьюмер не отстал от продюсера (проверьте тайминги)" >&2
    exit 1
  fi
  if [ "$final_lag" != "0" ]; then
    echo "[assert] FAIL: финальный лаг $final_lag != 0 — консьюмер не догнал backlog" >&2
    exit 1
  fi
  echo "[assert] OK: лаг вырос до $max_lag_seen > 0, затем упал ровно до 0"

  docker rm -f ops-lag-producer ops-lag-consumer >/dev/null 2>&1 || true
}

# --- Сценарий 2: under-replicated-partitions при падении НЕ-лидера реплики ---
scenario_under_replicated() {
  echo "=================================================================="
  echo "=== under-replicated-partitions: kill НЕ-лидера реплики $TOPIC ==="
  echo "=================================================================="

  if ! docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --list 2>/dev/null | grep -qx "$TOPIC"; then
    echo "--- топик $TOPIC не существует (сценарий growth-shrink ещё не запускался) — создаю пустым ---"
    docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
      --create --topic "$TOPIC" --partitions 3 --replication-factor 3 --config min.insync.replicas=2 2>&1 || true
  fi

  local desc
  desc=$(docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --topic "$TOPIC" 2>&1)
  echo "--- describe ДО kill ---"
  echo "$desc"

  local p0_line leader replicas follower
  p0_line=$(echo "$desc" | grep -E "Partition: 0[[:space:]]")
  leader=$(echo "$p0_line" | grep -oE 'Leader: [0-9]+' | grep -oE '[0-9]+')
  replicas=$(echo "$p0_line" | grep -oE 'Replicas: [0-9,]+' | grep -oE '[0-9,]+' | tr ',' ' ')
  for r in $replicas; do
    if [ "$r" != "$leader" ]; then follower="$r"; break; fi
  done
  echo "--- партиция 0: leader=$leader replicas=[$replicas] -> убиваю НЕ-лидера follower=$follower (kafka-cookbook-$follower) ---"

  docker kill "kafka-cookbook-$follower"
  # replica.lag.time.max.ms (дефолт 30000мс) — реплика считается выпавшей из
  # ISR (и партиция — под-реплицированной) только после того, как не
  # фетчила/не догоняла лидера дольше этого порога. Поэтому под-репликация
  # НЕ появляется мгновенно после kill — поллим до ~40с, а не спим фиксированные
  # 5-6с (живьём проверено: на 6с список ещё пуст).
  echo "--- жду обнаружения ISR-shrink контроллером (replica.lag.time.max.ms, дефолт 30с) ---"
  local urp=""
  for i in $(seq 1 14); do
    urp=$(docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
      --describe --under-replicated-partitions --topic "$TOPIC" 2>/dev/null || true)
    if echo "$urp" | grep -q "Topic: $TOPIC"; then
      echo "[poll $i, t=+$((i*3))s] под-репликация обнаружена"
      break
    fi
    echo "[poll $i, t=+$((i*3))s] ещё не обнаружена, жду..."
    sleep 3
  done

  echo "--- under-replicated-partitions ПОСЛЕ kill (ожидаем увидеть $TOPIC) ---"
  echo "$urp"
  if ! echo "$urp" | grep -q "Topic: $TOPIC"; then
    echo "[assert] FAIL: под-репликация НЕ обнаружена за 42с после kill" >&2
    docker start "kafka-cookbook-$follower" >/dev/null 2>&1 || true
    exit 1
  fi
  echo "[assert] OK: партиция(и) $TOPIC под-реплицированы после kill follower=$follower"

  echo "--- восстанавливаю kafka-cookbook-$follower ---"
  docker start "kafka-cookbook-$follower"
  wait_healthy "kafka-cookbook-$follower"

  echo "--- жду ISR resync (поллю до ~30с) ---"
  local after=""
  for i in $(seq 1 10); do
    after=$(docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
      --describe --under-replicated-partitions --topic "$TOPIC" 2>/dev/null || true)
    if ! echo "$after" | grep -q "Topic: $TOPIC"; then
      break
    fi
    sleep 3
  done
  echo "--- under-replicated-partitions ПОСЛЕ restore (ожидаем ПУСТО) ---"
  echo "$after"
  if echo "$after" | grep -q "Topic: $TOPIC"; then
    echo "[assert] FAIL: под-реплицированные партиции остались после restore" >&2
    exit 1
  fi
  echo "[assert] OK: под-реплицированных партиций после restore нет"
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
    growth-shrink) scenario_growth_shrink ;;
    under-replicated) scenario_under_replicated ;;
    all)
      scenario_growth_shrink
      scenario_under_replicated
      ;;
    *)
      echo "неизвестный сценарий: $scenario (growth-shrink|under-replicated|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО: lag-demo завершён (client=$CLIENT) ==="
}

main "$@"
