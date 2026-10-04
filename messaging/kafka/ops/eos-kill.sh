#!/usr/bin/env bash
# eos-kill.sh — оркестрирует сценарии стенда #5 ("exactly-once и транзакции").
#
# Сценарий "txn" (батч commit/abort/retry) самодостаточен — просто гоняет фазы
# по порядку. Сценарий "cpp" (consume-process-produce, ядро EOS) требует,
# чтобы ХОСТ реально убил (SIGKILL) процесс-клиент МЕЖДУ produce в output и
# commit транзакции — у клиента внутри контейнера нет доступа к docker socket,
# поэтому попытка A запускается ФОНОВЫМ контейнером (docker run -d), скрипт
# ждёт в её логах маркер "READY-TO-COMMIT" (клиент печатает его сразу после
# produce, ПЕРЕД паузой, и только потом вызывает session.End(commit)) и
# убивает контейнер РОВНО в этом окне — так же, как ../wal/postgres/recovery-test.sh
# убивает Postgres SIGKILL между записью WAL и следующим чекпоинтом, и как
# ops/broker-kill.sh убивает лидера партиции между produce и verify.
#
# После настоящего убийства попытка B стартует с ТЕМ ЖЕ TransactionalID и ТОЙ
# ЖЕ consumer-группой: BeginTransaction() попытки B (через InitProducerId под
# капотом) фенсит/абортит зависшую транзакцию попытки A на стороне брокера
# (KIP-98), а группа, чей офсет попытка A так и не закоммитила, отдаёт
# попытке B ТЕ ЖЕ самые записи заново — реальная демонстрация восстановления
# EOS без выдуманных чисел.
#
# Запуск: bash ops/eos-kill.sh [-client=go|java] <scenario>
#   -client: go (по умолчанию) | java
#   scenario: txn | cpp | all (по умолчанию)

set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."          # kafka/
GOBIN="go/bin/eos"
JAVA_JAR="java/eos/target/eos.jar"
CLIENT="${CLIENT:-go}"

require_binary() {
  case "$CLIENT" in
    go)
      if [ ! -x "$GOBIN" ]; then
        echo "[ops] $GOBIN не найден — собираю..."
        docker run --rm -v "$(pwd)/go:/app" -w /app golang:1.25 sh -c "go build -o bin/eos ./eos"
      fi
      ;;
    java)
      if [ ! -f "$JAVA_JAR" ]; then
        echo "[ops] $JAVA_JAR не найден — собираю..."
        docker run --rm -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
          sh -c "mvn -q -pl eos -am package -DskipTests"
      fi
      ;;
  esac
}

# jargs — транслирует "-key=value" (Go-стиль флагов этого стенда) в
# "--key=value" (парсер Main.java, тот же формат, что replication/storage).
jargs() {
  local a
  for a in "$@"; do
    case "$a" in
      -*) printf '%s\n' "-$a" ;;
      *)  printf '%s\n' "$a" ;;
    esac
  done
}

# client_run — один фореграунд-вызов фазы, ждёт завершения, отдаёт stdout.
client_run() {
  case "$CLIENT" in
    java)
      local out=() a
      while IFS= read -r a; do out+=("$a"); done < <(jargs "$@")
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
        java -jar /app/eos/target/eos.jar "${out[@]}"
      ;;
    *)
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/eos "$@"
      ;;
  esac
}

# client_run_bg NAME ARGS... — фоновый контейнер с заданным именем (для
# попытки A, которую собираемся убить). Возвращает сразу, не дожидаясь
# завершения процесса внутри.
client_run_bg() {
  local name="$1"; shift
  docker rm -f "$name" >/dev/null 2>&1 || true
  case "$CLIENT" in
    java)
      local out=() a
      while IFS= read -r a; do out+=("$a"); done < <(jargs "$@")
      docker run -d --name "$name" --network kafka-cookbook-net -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
        java -jar /app/eos/target/eos.jar "${out[@]}" >/dev/null
      ;;
    *)
      docker run -d --name "$name" --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/eos "$@" >/dev/null
      ;;
  esac
}

# wait_for_marker NAME MARKER TIMEOUT_S — опрашивает docker logs, пока не
# увидит строку с MARKER либо не истечёт TIMEOUT_S секунд.
wait_for_marker() {
  local name="$1" marker="$2" timeout="$3"
  local i
  for i in $(seq 1 "$timeout"); do
    if docker logs "$name" 2>&1 | grep -q "$marker"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# --- Сценарий "txn": батч A commit, батч B abort->retry-commit, read_committed
# vs read_uncommitted count. Самодостаточен, без docker kill.
scenario_txn() {
  echo "=================================================================="
  echo "=== txn: батч A commit, батч B abort+retry, read_committed vs read_uncommitted ==="
  echo "=================================================================="
  local topic=demo-eos-txn
  local batch=5

  client_run -scenario=txn-setup -topic="$topic" -partitions=3 -rf=3
  client_run -scenario=txn-run -topic="$topic" -batch-size="$batch"
  # физически: batchA(5) + batchB-попытка1(3, оборвана на i==2) + batchB-повтор(5) = 13
  # логически: batchA(5) + batchB-повтор(5) = 10
  client_run -scenario=txn-verify -topic="$topic" -expect-committed=$((batch * 2)) -expect-physical=$((batch + 3 + batch))
}

# --- Сценарий "cpp": атомарный consume-process-produce, с РЕАЛЬНЫМ SIGKILL
# ровно между produce-в-output и commit.
scenario_cpp() {
  echo "=================================================================="
  echo "=== cpp: consume-process-produce, SIGKILL ДО commit, атомарность ==="
  echo "=================================================================="
  local input=demo-eos-cpp-input output=demo-eos-cpp-output group=eos-cpp-group
  local txn=cookbook-eos-cpp-producer n=10
  local cname="eos-cpp-attempt-a-$CLIENT"

  client_run -scenario=cpp-setup -input-topic="$input" -output-topic="$output" -group="$group" -partitions=3 -rf=3
  client_run -scenario=cpp-seed -input-topic="$input" -n="$n" -prefix=cpp

  echo "--- попытка A (фон): consume+process+produce, ЗАТЕМ пауза 60с ДО commit ---"
  client_run_bg "$cname" -scenario=cpp-attempt -group="$group" -txn-id="$txn" -input-topic="$input" -output-topic="$output" -n="$n" -pause=60s

  if ! wait_for_marker "$cname" "READY-TO-COMMIT" 30; then
    echo "[ops] МАРКЕР READY-TO-COMMIT не появился за 30с — логи попытки A:" >&2
    docker logs "$cname" >&2 || true
    docker rm -f "$cname" >/dev/null 2>&1 || true
    exit 1
  fi
  echo "--- маркер READY-TO-COMMIT пойман — docker kill попытки A (SIGKILL, ДО commit) ---"
  docker kill "$cname"
  echo "--- логи убитой попытки A (обрыв ровно на маркере, без строки commit) ---"
  docker logs "$cname"
  local exitcode
  exitcode=$(docker inspect "$cname" --format '{{.State.ExitCode}}')
  echo "--- exit code попытки A: $exitcode (137 = SIGKILL, реальный крах) ---"
  docker rm -f "$cname" >/dev/null 2>&1 || true

  echo "--- verify ДО повтора: output НЕВИДИМ read_committed (0), офсет группы НЕ продвинут (0) ---"
  client_run -scenario=cpp-verify -group="$group" -input-topic="$input" -output-topic="$output" \
    -label=после-kill-до-повтора -expect-output-committed=0 -expect-group-offset=0

  echo "--- попытка B: ТОТ ЖЕ txn-id (фенсит зависшую транзакцию A) и ТА ЖЕ группа (перечитывает те же записи), commit сразу ---"
  client_run -scenario=cpp-attempt -group="$group" -txn-id="$txn" -input-topic="$input" -output-topic="$output" -n="$n" -pause=0

  echo "--- verify ПОСЛЕ повтора: output = n РОВНО ОДИН РАЗ, офсет группы = n ---"
  client_run -scenario=cpp-verify -group="$group" -input-topic="$input" -output-topic="$output" \
    -label=после-повтора -expect-output-committed="$n" -expect-group-offset="$n"
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
    txn) scenario_txn ;;
    cpp) scenario_cpp ;;
    all)
      scenario_txn
      scenario_cpp
      ;;
    *)
      echo "неизвестный сценарий: $scenario (txn|cpp|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО ==="
}

main "$@"
