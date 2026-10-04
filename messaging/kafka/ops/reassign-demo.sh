#!/usr/bin/env bash
# reassign-demo.sh — оркестрирует часть стенда #6 ("эксплуатация"): ручную
# перебалансировку партиций (kafka-reassign-partitions.sh — генерируем JSON,
# исполняем с throttle, verify, describe до/после) и автоматическую
# (auto.leader.rebalance.enable — авто-возврат leadership к preferred-реплике
# после восстановления брокера).
#
# Оба сценария — чистая работа над БРОКЕРОМ/кластером через kafka-*.sh CLI
# (docker exec), без клиентского Go/Java-кода — реассайнмент и leader
# rebalance это операции контроллера, не producer/consumer API (см.
# ../go/ops/main.go, комментарий вверху файла). Топик для реассайнмента
# сеется данными через ../go/ops (или ../java/ops) -scenario=seed, чтобы
# реассайнмент реально копировал байты, а не пустые партиции.
#
# ⚠️ auto-leader-rebalance зависит от KAFKA_LEADER_IMBALANCE_CHECK_INTERVAL_SECONDS=5
# в ../compose/compose.yml (дефолт брокера 300с/5мин, НЕ dynamic-alterable —
# тот же случай, что log.retention.check.interval.ms в стенде #4). Если
# кластер поднят БЕЗ этого статического конфига (старый compose.yml) —
# сценарий auto-leader либо не завершится за разумное время, либо завершится
# нечестно "предположительно" — сценарий явно проверяет текущее значение и
# падает с понятным сообщением, если оно не занижено.
#
# Запуск: bash ops/reassign-demo.sh [-client=go|java] <scenario>
#   scenario: manual | auto-leader | all (по умолчанию)

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # kafka/
GOBIN="go/bin/ops"
JAVA_JAR="java/ops/target/ops.jar"
CLIENT="${CLIENT:-go}"

REASSIGN_TOPIC="demo-ops-reassign"
LEADER_TOPIC="demo-ops-leader"
BROKER="kafka-cookbook-1"   # любой живой брокер как bootstrap для CLI-команд

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

seed_topic() {
  local topic="$1" partitions="$2" rf="$3" n="$4"
  case "$CLIENT" in
    java)
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
        java -jar /app/ops/target/ops.jar --scenario=seed --topic="$topic" --recreate=true --partitions="$partitions" --rf="$rf" --minisr=1 --n="$n" --value-bytes=300
      ;;
    *)
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 \
        /app/ops -scenario=seed -topic="$topic" -recreate=true -partitions="$partitions" -rf="$rf" -minisr=1 -n="$n" -value-bytes=300
      ;;
  esac
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

describe_topic() {
  local topic="$1" exec_broker="${2:-$BROKER}"
  docker exec "$exec_broker" /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --describe --topic "$topic" 2>&1
}

# safe_broker EXCLUDE_ID — имя compose-контейнера любого брокера, КРОМЕ
# EXCLUDE_ID (нужно, когда сам $BROKER — это тот узел, который мы вот-вот
# убьём: kafka-*.sh CLI нельзя выполнить через `docker exec` в контейнере,
# который уже остановлен/убит).
safe_broker() {
  local exclude="$1" id
  for id in 1 2 3; do
    if [ "$id" != "$exclude" ]; then
      echo "kafka-cookbook-$id"
      return 0
    fi
  done
}

# --- Сценарий 1: ручной реассайнмент партиций (kafka-reassign-partitions.sh) ---
scenario_manual() {
  echo "=================================================================="
  echo "=== ручной reassignment: $REASSIGN_TOPIC (partitions=3, rf=2) ==="
  echo "=================================================================="

  echo "--- сею $REASSIGN_TOPIC реальными данными (2000 записей) ---"
  seed_topic "$REASSIGN_TOPIC" 3 2 2000

  local before
  before=$(describe_topic "$REASSIGN_TOPIC")
  echo "--- describe ДО reassignment ---"
  echo "$before"

  echo "--- строю reassignment JSON: сдвигаю каждый broker-id реплики на +1 (mod 3, 1-based) ---"
  local json='{"version":1,"partitions":['
  local first=1 part replicas ids new_replicas nid
  while IFS= read -r line; do
    case "$line" in *"Partition:"*) ;; *) continue ;; esac
    part=$(echo "$line" | grep -oE 'Partition: [0-9]+' | grep -oE '[0-9]+')
    replicas=$(echo "$line" | grep -oE 'Replicas: [0-9,]+' | grep -oE '[0-9,]+')
    new_replicas=""
    IFS=',' read -ra ids <<< "$replicas"
    for id in "${ids[@]}"; do
      nid=$(( id % 3 + 1 ))
      new_replicas="${new_replicas}${nid},"
    done
    new_replicas="${new_replicas%,}"
    if [ $first -eq 0 ]; then json="${json},"; fi
    first=0
    json="${json}{\"topic\":\"$REASSIGN_TOPIC\",\"partition\":$part,\"replicas\":[$new_replicas]}"
    echo "  partition=$part: [$replicas] -> [$new_replicas]"
  done <<< "$before"
  json="${json}]}"

  echo "--- reassignment JSON ---"
  echo "$json"
  printf '%s' "$json" | docker exec -i "$BROKER" sh -c "cat > /tmp/ops-reassign.json"

  echo "--- execute (throttle=1000000 байт/с, чтобы движение было наблюдаемым) ---"
  docker exec "$BROKER" /opt/kafka/bin/kafka-reassign-partitions.sh --bootstrap-server localhost:9092 \
    --reassignment-json-file /tmp/ops-reassign.json --execute --throttle 1000000 2>&1

  # --verify печатает построчно "Reassignment of partition X is completed."
  # (готово) или "...is still in progress" (в процессе) на каждую партицию;
  # по завершении ВСЕХ партиций дополнительно печатает "Clearing
  # broker-level/topic-level throttles..." — проверено живьём на
  # scratch-топике перед написанием этого скрипта.
  echo "--- поллю --verify до завершения (до ~60с) ---"
  local verify_out=""
  for i in $(seq 1 20); do
    verify_out=$(docker exec "$BROKER" /opt/kafka/bin/kafka-reassign-partitions.sh --bootstrap-server localhost:9092 \
      --reassignment-json-file /tmp/ops-reassign.json --verify 2>&1)
    echo "[verify poll $i]"
    echo "$verify_out"
    if ! echo "$verify_out" | grep -qi "still in progress"; then
      echo "--- reassignment завершён (все партиции 'is completed') ---"
      break
    fi
    sleep 3
  done

  local after
  after=$(describe_topic "$REASSIGN_TOPIC")
  echo "--- describe ПОСЛЕ reassignment ---"
  echo "$after"

  echo "--- сравнение размещения (партиция: было -> стало) ---"
  local moved=0
  while IFS= read -r bline; do
    case "$bline" in *"Partition:"*) ;; *) continue ;; esac
    local p old new
    p=$(echo "$bline" | grep -oE 'Partition: [0-9]+' | grep -oE '[0-9]+')
    old=$(echo "$bline" | grep -oE 'Replicas: [0-9,]+')
    new=$(echo "$after" | grep -E "Partition: $p[[:space:]]" | grep -oE 'Replicas: [0-9,]+')
    echo "  partition=$p: $old -> $new"
    if [ "$old" != "$new" ]; then moved=$((moved+1)); fi
  done <<< "$before"

  if [ "$moved" -eq 0 ]; then
    echo "[assert] FAIL: ни одна партиция не сменила размещение реплик" >&2
    exit 1
  fi
  echo "[assert] OK: $moved из 3 партиций сменили размещение реплик (реассайнмент реально переместил данные)"
}

# --- Сценарий 2: auto.leader.rebalance.enable — авто-возврат leadership ---
scenario_auto_leader() {
  echo "=================================================================="
  echo "=== auto-leader-rebalance: $LEADER_TOPIC (partitions=3, rf=3) ==="
  echo "=================================================================="

  local interval
  # ⚠️ Строка вывода kafka-configs.sh для этого ключа содержит ЗНАЧЕНИЕ ТРИЖДЫ
  # (текущее + synonyms STATIC_BROKER_CONFIG + synonyms DEFAULT_CONFIG), напр.
  # "leader.imbalance.check.interval.seconds=5 ... synonyms={STATIC_BROKER_CONFIG:...=5, DEFAULT_CONFIG:...=300}"
  # — берём ТОЛЬКО первое вхождение (head -1), это и есть действующее значение.
  interval=$(docker exec "$BROKER" /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
    --entity-type brokers --entity-name 1 --describe --all 2>&1 \
    | grep -oE 'leader\.imbalance\.check\.interval\.seconds=[0-9]+' | head -1 | grep -oE '[0-9]+$')
  echo "--- leader.imbalance.check.interval.seconds=$interval (см. compose.yml) ---"
  if [ -z "$interval" ] || [ "$interval" -gt 30 ]; then
    echo "[ops] ПРЕДУПРЕЖДЕНИЕ: интервал проверки дисбаланса контроллером = ${interval:-неизвестно}с — слишком долго для живой демонстрации в разумное время. Ожидался статический конфиг KAFKA_LEADER_IMBALANCE_CHECK_INTERVAL_SECONDS=5 в compose.yml (см. commit истории стенда #6) и 'docker compose up -d' (recreate) ПОСЛЕ его добавления. Прерываю честно, не жду 5 минут вслепую." >&2
    exit 1
  fi

  echo "--- сею $LEADER_TOPIC (RF=3 — переживаем kill одного брокера без потери доступности) ---"
  seed_topic "$LEADER_TOPIC" 3 3 200

  local before
  before=$(describe_topic "$LEADER_TOPIC")
  echo "--- describe ДО kill (текущие лидеры = preferred, только что созданы) ---"
  echo "$before"

  local p0_line preferred
  p0_line=$(echo "$before" | grep -E "Partition: 0[[:space:]]")
  # Replicas: "N,M,K" -> первый элемент = preferred (replicas[0])
  preferred=$(echo "$p0_line" | grep -oE 'Replicas: [0-9,]+' | sed -E 's/Replicas: ([0-9]+).*/\1/')
  echo "--- партиция 0: preferred (replicas[0]) leader = $preferred ---"

  echo "--- docker kill kafka-cookbook-$preferred (форсирую failover leadership на другую реплику) ---"
  docker kill "kafka-cookbook-$preferred"

  # $BROKER мог оказаться ТЕМ САМЫМ узлом, который мы только что убили —
  # docker exec в него больше не сработает, пока не восстановим. Используем
  # любой ДРУГОЙ живой брокер как CLI bootstrap на время окна kill.
  local alt_broker
  alt_broker=$(safe_broker "$preferred")
  echo "--- CLI bootstrap на время окна kill: $alt_broker ---"

  echo "--- жду failover (leader партиции 0 меняется) ---"
  local failover_leader=""
  for i in $(seq 1 15); do
    local d line l
    d=$(describe_topic "$LEADER_TOPIC" "$alt_broker" 2>/dev/null || true)
    line=$(echo "$d" | grep -E "Partition: 0[[:space:]]" || true)
    l=$(echo "$line" | grep -oE 'Leader: [0-9]+' | grep -oE '[0-9]+')
    if [ -n "$l" ] && [ "$l" != "$preferred" ] && [ "$l" != "-1" ]; then
      failover_leader="$l"
      echo "[poll $i] failover подтверждён: leader=$l"
      break
    fi
    echo "[poll $i] leader=${l:-?} (жду смены с $preferred)"
    sleep 2
  done
  if [ -z "$failover_leader" ]; then
    echo "[assert] FAIL: failover не произошёл за отведённое время" >&2
    docker start "kafka-cookbook-$preferred" >/dev/null 2>&1 || true
    exit 1
  fi

  echo "--- восстанавливаю kafka-cookbook-$preferred ---"
  docker start "kafka-cookbook-$preferred"
  wait_healthy "kafka-cookbook-$preferred"

  echo "--- жду авто-возврата leadership к preferred=$preferred (auto.leader.rebalance.enable=true, проверка каждые ${interval}с) ---"
  local returned=""
  for i in $(seq 1 20); do
    local d line l
    d=$(describe_topic "$LEADER_TOPIC" 2>/dev/null || true)
    line=$(echo "$d" | grep -E "Partition: 0[[:space:]]" || true)
    l=$(echo "$line" | grep -oE 'Leader: [0-9]+' | grep -oE '[0-9]+')
    echo "[poll $i, t=+$((i*3))s] leader партиции 0 = ${l:-?}"
    if [ "$l" = "$preferred" ]; then
      returned="yes"
      break
    fi
    sleep 3
  done

  echo "--- describe ПОСЛЕ (ожидаем leader партиции 0 == preferred == $preferred) ---"
  describe_topic "$LEADER_TOPIC"

  if [ -z "$returned" ]; then
    echo "[assert] FAIL: leadership НЕ вернулось к preferred=$preferred за отведённое время" >&2
    exit 1
  fi
  echo "[assert] OK: leadership партиции 0 вернулось к preferred-реплике $preferred автоматически (без ручного kafka-leader-election.sh)"
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
    manual) scenario_manual ;;
    auto-leader) scenario_auto_leader ;;
    all)
      scenario_manual
      scenario_auto_leader
      ;;
    *)
      echo "неизвестный сценарий: $scenario (manual|auto-leader|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО: reassign-demo завершён (client=$CLIENT) ==="
}

main "$@"
