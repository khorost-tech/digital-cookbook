#!/usr/bin/env bash
# broker-kill.sh — оркестрирует сценарии стенда #3 ("репликация и надёжность"),
# которые требуют убивать/поднимать брокеров docker-командами С ХОСТА (у
# Go/Java клиента внутри контейнера нет доступа к docker socket, поэтому
# сами клиенты разбиты на "фазы" — см. ../go/replication и ../java/replication).
#
# Аналог WAL SIGKILL-стенда (../../wal/postgres/recovery-test.sh): count
# до/после жёсткого падения. Здесь — count acked-сообщений до/после падения
# ЛИДЕРА партиции, при живом кластере (RF=3, ISR-репликация).
#
# Требует: кластер поднят (docker compose -f ../compose/compose.yml up -d),
# собранный Go-бинарь ../go/bin/replication (см. ниже) или java -jar.
#
# ⚠️ Используем `docker kill` (SIGKILL, без grace period), а не `docker stop`
# (SIGTERM с graceful shutdown до 10с) — это осознанный выбор: Kafka на
# SIGTERM выполняет controlled shutdown, который САМ переносит лидерство на
# другую реплику ДО завершения процесса — это "красивое" отключение, не
# настоящий крах. `docker kill` эмулирует реальное падение ноды (питание,
# OOM-killer, kill -9) — без предупреждения кластеру, ровно то, что должен
# пережить ISR-failover. Тот же выбор сделан в WAL-стенде
# (docker compose kill -s SIGKILL).
#
# Запуск: bash ops/broker-kill.sh [-client=go|java] <scenario>
#   -client: go (по умолчанию) | java — какой клиент гоняет фазы (флаг или env CLIENT)
#   scenario: durability | minisr-literal | minisr-contrast | all (по умолчанию)
#
# Примеры:
#   bash ops/broker-kill.sh durability                # Go (дефолт)
#   bash ops/broker-kill.sh -client=java minisr-contrast
#   CLIENT=java bash ops/broker-kill.sh all
#
# Каждый под-сценарий сам восстанавливает убитых брокеров в конце (`docker start`)
# и ждёт healthy — так что кластер остаётся исправным после прогона.

set -euo pipefail
export MSYS_NO_PATHCONV=1        # Git Bash on Windows: не переписывать unix-style пути в docker-аргументах
cd "$(dirname "$0")/.."          # kafka/
COMPOSE="compose/compose.yml"
GOBIN="go/bin/replication"
JAVA_JAR="java/replication/target/replication.jar"
CLIENT="${CLIENT:-go}"           # go|java — переопределяется флагом -client/--client в main()

require_binary() {
  case "$CLIENT" in
    go)
      if [ ! -x "$GOBIN" ]; then
        echo "[ops] $GOBIN не найден — собираю..."
        docker run --rm -v "$(pwd)/go:/app" -w /app golang:1.25 sh -c "go build -o bin/replication ./replication"
      fi
      ;;
    java)
      if [ ! -f "$JAVA_JAR" ]; then
        echo "[ops] $JAVA_JAR не найден — собираю..."
        docker run --rm -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
          sh -c "mvn -q -pl replication -am package -DskipTests"
      fi
      ;;
  esac
}

# client_run — запускает одну фазу выбранного клиента. Флаги пробрасываются
# в Go-стиле (-key=value, как принимает пакет flag стенда); при CLIENT=java
# каждый аргумент, начинающийся с одного "-", транслируется в "--key=value"
# (см. Main.java: парсер флагов ожидает ровно двойной дефис) — так тела
# сценариев ниже написаны один раз и не знают, какой клиент их выполняет.
client_run() {
  case "$CLIENT" in
    java)
      local jargs=() a
      for a in "$@"; do
        case "$a" in
          -*) jargs+=("-$a") ;;   # "-scenario=x" -> "--scenario=x"
          *)  jargs+=("$a") ;;
        esac
      done
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
        java -jar /app/replication/target/replication.jar "${jargs[@]}"
      ;;
    *)
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/replication "$@"
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

# --- Сценарий 1: durability failover (acks=all, kill leader, count до==после) ---
scenario_durability() {
  echo "=================================================================="
  echo "=== durability: acks=all, kill leader, count до==после (failover) ==="
  echo "=================================================================="
  local topic=demo-repl-durability

  client_run -scenario=setup -topic="$topic" -partitions=1 -rf=3 -minisr=2

  echo "--- produce 30 сообщений acks=all ДО падения ---"
  client_run -scenario=produce -topic="$topic" -n=30 -acks=all -idempotent=true -prefix=durable

  local before
  before=$(client_run -scenario=describe -topic="$topic")
  echo "--- состояние ДО kill: $before ---"
  local leader_id
  leader_id=$(echo "$before" | grep -oE 'leader=[0-9]+' | grep -oE '[0-9]+')
  local container="kafka-cookbook-$leader_id"

  echo "--- docker kill $container (SIGKILL, реальный крах без graceful shutdown) ---"
  docker kill "$container"

  # Детекция failover — по смене leader (поле "leader=N(...)" идентично
  # формату Go и Java, в отличие от replicas/isr/offline — там форматы
  # клиентов расходятся: Go печатает offline=[...], Java — нет вовсе), так
  # проверка остаётся клиенто-независимой.
  echo "--- жду обнаружения падения контроллером и переизбрания лидера ---"
  for i in $(seq 1 10); do
    out=$(client_run -scenario=describe -topic="$topic")
    echo "[poll $i] $out"
    new_leader=$(echo "$out" | grep -oE 'leader=[0-9]+' | grep -oE '[0-9]+')
    if [ -n "$new_leader" ] && [ "$new_leader" != "$leader_id" ]; then
      echo "--- failover подтверждён (лидер сменился $leader_id -> $new_leader) ---"
      break
    fi
    sleep 2
  done

  echo "--- verify: count после failover должен == 30 (без потерь acked) ---"
  client_run -scenario=verify -topic="$topic" -expect=30

  echo "--- восстанавливаю $container ---"
  docker start "$container"
  wait_healthy "$container"
}

# --- Сценарий 2а: min.insync.replicas, буквально "убить 2 из 3" (RF=3) ---
# ⚠️ Реальный наблюдаемый результат — НЕ чистый NOT_ENOUGH_REPLICAS: в этой
# топологии (3 combined broker+controller ноды, все — voters контроллерного
# кворума) убийство 2 из 3 ломает МАЖОРИТАРНОСТЬ кворума контроллера тоже —
# ISR-shrink не может быть подтверждён контроллером, поэтому producer виснет
# и падает по таймауту/исчерпанию ретраев, а не по чистому коду ошибки. См.
# README.md (раздел «Проверено живьём») за разбор и подтверждающие живые логи.
scenario_minisr_literal() {
  echo "=================================================================="
  echo "=== minisr (буквально): kill 2 из 3 на RF=3 — контроллер теряет кворум ==="
  echo "=================================================================="
  local topic=demo-repl-minisr-literal

  client_run -scenario=setup -topic="$topic" -partitions=1 -rf=3 -minisr=2
  local leader
  leader=$(client_run -scenario=describe -topic="$topic")
  echo "--- состояние ДО kill: $leader ---"

  echo "--- убиваю 2 брокера (оставляю живым только один — оба варианта: лидер выживает или нет, результат один) ---"
  docker kill kafka-cookbook-2 kafka-cookbook-3
  sleep 3

  echo "--- describe (ISR НЕ подтверждён контроллером — останется устаревшим) ---"
  client_run -scenario=describe -topic="$topic" || true

  echo "--- попытка acks=all (ожидаем таймаут/исчерпание ретраев, НЕ чистый NOT_ENOUGH_REPLICAS) ---"
  client_run -scenario=minisr-produce -topic="$topic" -acks=all || true

  echo "--- восстанавливаю кластер ---"
  docker start kafka-cookbook-2 kafka-cookbook-3
  wait_healthy kafka-cookbook-2
  wait_healthy kafka-cookbook-3
}

# --- Сценарий 2б: min.insync.replicas, контраст — RF=2, kill 1 из 2 реплик,
# кворум контроллера ЦЕЛ (жив 2 из 3 брокеров) — чистый NOT_ENOUGH_REPLICAS.
scenario_minisr_contrast() {
  echo "=================================================================="
  echo "=== minisr (контраст): RF=2, kill 1 реплику, кворум контроллера цел ==="
  echo "=================================================================="
  local topic=demo-repl-minisr-contrast

  client_run -scenario=setup -topic="$topic" -partitions=1 -rf=2 -minisr=2
  local state
  state=$(client_run -scenario=describe -topic="$topic")
  echo "--- состояние ДО kill: $state ---"
  # [0-9, ]+ — replicas печатается как "[1 2]" (Go, через пробел) или
  # "[1, 2]" (Java, через запятую+пробел); класс символов покрывает оба.
  local replica2
  replica2=$(echo "$state" | grep -oE 'replicas=\[[0-9, ]+\]' | grep -oE '[0-9]+' | tail -1)
  local container="kafka-cookbook-$replica2"

  echo "--- убиваю одну реплику ($container), вторую (не-реплику) не трогаю — кворум контроллера остаётся 2 из 3 ---"
  docker kill "$container"

  echo "--- жду ISR-shrink (replica.lag.time.max.ms) ---"
  for i in $(seq 1 15); do
    out=$(client_run -scenario=describe -topic="$topic")
    echo "[poll $i] $out"
    # isr=[N] в конце строки (Java, без offline=) либо перед пробелом
    # (Go, за которым следует offline=[...]) — оба случая покрыты.
    if echo "$out" | grep -qE 'isr=\[[0-9]+\]( |$)'; then
      echo "ISR сжался до одного элемента — контроллер подтвердил shrink"
      break
    fi
    sleep 3
  done

  echo "--- попытка acks=all (ожидаем ЧИСТЫЙ NOT_ENOUGH_REPLICAS) ---"
  client_run -scenario=minisr-produce -topic="$topic" -acks=all || true

  echo "--- восстанавливаю $container ---"
  docker start "$container"
  wait_healthy "$container"
}

main() {
  # необязательный префикс -client=go|java (или --client=..., -client X) перед сценарием
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
    durability) scenario_durability ;;
    minisr-literal) scenario_minisr_literal ;;
    minisr-contrast) scenario_minisr_contrast ;;
    all)
      scenario_durability
      scenario_minisr_literal
      scenario_minisr_contrast
      ;;
    *)
      echo "неизвестный сценарий: $scenario (durability|minisr-literal|minisr-contrast|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО: кластер восстановлен, все брокеры healthy ==="
}

main "$@"
