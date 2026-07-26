#!/usr/bin/env bash
# ecosystem-demo.sh — оркестрирует стенд #7 ("экосистема: Kafka Connect +
# Schema Registry") поверх УЖЕ поднятого 3-брокерного KRaft-кластера
# (../compose/compose.yml). В отличие от стендов #3/#4/#6, здесь НЕТ
# отдельного Go/Java-клиента, который "дирижирует" REST-скрипт: наоборот,
# сам этот bash-скрипт — единственный клиент (curl к REST API Schema
# Registry и Kafka Connect), кроме части 3 (сериализация), которая зовёт
# ../go/ecosystem/serialization напрямую.
#
# Поднимает (../compose/ecosystem.yml, поверх сети kafka-cookbook-net):
#   - schema-registry: apicurio/apicurio-registry, storage=kafkasql (хранит
#     схемы В ЭТОМ ЖЕ apache/kafka-кластере, топики kafkasql-journal/
#     kafkasql-snapshots/registry-events) — выбор и почему НЕ Confluent
#     cp-schema-registry см. ../README.md.
#   - kafka-connect: тот же образ apache/kafka:4.3.1, что и брокеры,
#     connect-distributed.sh "из коробки" (FileStream-коннекторы + SMT уже
#     в libs/ образа, отдельный плагин не нужен).
#
# Требует: кластер поднят (docker compose -f ../compose/compose.yml up -d),
# python на хосте (только для корректного построения JSON-тела REST-запроса
# регистрации схемы — см. content-note ниже про поломанное экранирование
# через sed/awk в Git Bash на Windows), собранный Go-бинарь
# ../go/bin/ecosystem-serialization (собирается сам при первом запуске).
#
# ⚠️ Живая находка: Git Bash (MSYS) на Windows в этом окружении ломает
# многосимвольные sed/awk-выражения с обратным слэшем в backslash-классах
# (`sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'` падает с "unterminated `s' command",
# `awk gsub(/\\/,...)` — с "Unmatched ( or \("), даже если та же команда
# синтаксически корректна и работает в обычном bash/WSL. Первая попытка
# построить JSON-тело регистрации схемы через sed молча "проглотила" ошибку
# (`set -e` её не ловит внутри `$(...)` командной подстановки без
# дополнительной проверки) и отправила в Schema Registry ПУСТУЮ строку схемы —
# реестр принял её как валидную (!), создав subject с schemaType=PROTOBUF и
# пустым телом, что затем ломало ВСЕ последующие попытки зарегистрировать
# по-настоящему Avro-схему в тот же subject ("Cannot parse <null> schema").
# Исправлено: тело JSON строится через `python -c` (json.dumps) — корректно
# экранирует кавычки/переносы строк независимо от платформы, без хрупкого
# sed/awk. Это ЕДИНСТВЕННОЕ место во всём стенде, где требуется python на
# хосте (остальные ops/*.sh репозитория — чистый bash+docker).
#
# Запуск: bash ops/ecosystem-demo.sh <scenario>
#   scenario: schema-registry | connect | serialization | all (по умолчанию)
#
# Cleanup: bash ops/ecosystem-demo.sh cleanup
#   удаляет коннекторы + subject Schema Registry + рантайм-файлы
#   ecosystem/connect-data/, оставляет schema-registry/kafka-connect
#   контейнеры ЖИВЫМИ (для повторных прогонов) — кластер brokers НЕ трогает.
#   Полный снос доп-сервисов: docker compose -f compose/compose.yml
#   -f compose/ecosystem.yml rm -sf schema-registry kafka-connect

set -euo pipefail
export MSYS_NO_PATHCONV=1        # Git Bash on Windows: не переписывать unix-style пути в docker-аргументах
cd "$(dirname "$0")/.."          # kafka/
COMPOSE_FILES="-f compose/compose.yml -f compose/ecosystem.yml"
SR="http://127.0.0.1:18080"      # ⚠️ localhost:18080 на этой машине временами не резолвится
CONNECT="http://127.0.0.1:18083" # с хоста (тот же баг, что описан в README про localhost:19092-19094) — используем 127.0.0.1 напрямую.
SUBJECT="ecosystem-user-value"
GOBIN="go/bin/ecosystem-serialization"
SCRATCH="ecosystem/connect-data/.demo-scratch"  # внутри уже gitignored ecosystem/connect-data/

# curl_r — обёртка над curl с ретраями. ⚠️ Живая находка: на этой машине
# localhost/127.0.0.1-подключения к сервисам стенда изредка "зависают" на
# ~20с и обрываются (тот же класс проблемы, что описан в README про
# localhost:19092-19094 — особенность конкретной машины/ОС, не конфигурации
# сервисов: docker exec на тех же контейнерах никогда не подвисает).
# Без --retry-all-errors: дефолтные ретраи curl уже покрывают обрыв
# соединения (наш случай) — --retry-all-errors заодно ретраил бы и ЗАКОНОМЕРНЫЕ
# 4xx-ответы (например, намеренный 409 при регистрации несовместимой схемы
# ниже), что просто тратило бы время без изменения результата.
curl_r() {
  curl -sS --retry 5 --retry-delay 2 --max-time 15 "$@"
}

wait_healthy() {
  local name="$1" tries=0
  echo "[ops] жду healthy: $name"
  while [ "$(docker inspect "$name" --format '{{.State.Health.Status}}' 2>/dev/null)" != "healthy" ]; do
    tries=$((tries + 1))
    if [ "$tries" -gt 40 ]; then
      echo "[ops] $name не стал healthy за отведённое время" >&2
      docker logs "$name" 2>&1 | tail -30
      exit 1
    fi
    sleep 3
  done
  echo "[ops] $name healthy"
}

up_ecosystem() {
  echo "--- поднимаю schema-registry + kafka-connect (поверх уже живого кластера) ---"
  docker compose $COMPOSE_FILES up -d schema-registry kafka-connect
  wait_healthy kafka-cookbook-schema-registry
  wait_healthy kafka-cookbook-connect
}

# build_schema_body <avsc-файл> — {"schema": "...", "schemaType": "AVRO"} с
# корректным JSON-экранированием (см. ⚠️ живую находку про sed/awk выше).
build_schema_body() {
  python -c "
import json, sys
with open(sys.argv[1], encoding='utf-8') as f:
    schema = f.read()
print(json.dumps({'schema': schema, 'schemaType': 'AVRO'}))
" "$1"
}

# --- Часть 1: Schema Registry — регистрация + эволюция (BACKWARD) ---
scenario_schema_registry() {
  echo "=================================================================="
  echo "=== Schema Registry (apicurio, ccompat v7 API): регистрация + эволюция ==="
  echo "=================================================================="
  mkdir -p "$SCRATCH"

  echo "--- на всякий случай подчищаю subject $SUBJECT от предыдущих прогонов ---"
  curl_r -X DELETE "$SR/apis/ccompat/v7/subjects/$SUBJECT" >/dev/null 2>&1 || true
  curl_r -X DELETE "$SR/apis/ccompat/v7/subjects/$SUBJECT?permanent=true" >/dev/null 2>&1 || true

  echo "--- (0) выставляю compatibility=BACKWARD для subject $SUBJECT ---"
  curl_r -X PUT -H "Content-Type: application/json" \
    -d '{"compatibility":"BACKWARD"}' "$SR/apis/ccompat/v7/config/$SUBJECT"
  echo

  build_schema_body ecosystem/schemas/user-v1.avsc > "$SCRATCH/v1.json"
  build_schema_body ecosystem/schemas/user-v2-compatible.avsc > "$SCRATCH/v2.json"
  build_schema_body ecosystem/schemas/user-v3-incompatible.avsc > "$SCRATCH/v3.json"

  echo "--- (1) регистрирую v1 (id/name/email) ---"
  curl_r -w "\nHTTP_STATUS:%{http_code}\n" -X POST \
    -H "Content-Type: application/vnd.schemaregistry.v1+json" \
    -d @"$SCRATCH/v1.json" "$SR/apis/ccompat/v7/subjects/$SUBJECT/versions"
  echo

  echo "--- (2) регистрирую v2: +age optional (default=null) — ДОЛЖНО пройти (BACKWARD-совместимо) ---"
  curl_r -w "\nHTTP_STATUS:%{http_code}\n" -X POST \
    -H "Content-Type: application/vnd.schemaregistry.v1+json" \
    -d @"$SCRATCH/v2.json" "$SR/apis/ccompat/v7/subjects/$SUBJECT/versions"
  echo

  echo "--- (3) регистрирую v3: тип id меняется long -> string — ДОЛЖНО быть ОТКЛОНЕНО (409) ---"
  local v3_status
  v3_status=$(curl_r -o "$SCRATCH/v3-response.json" -w "%{http_code}" -X POST \
    -H "Content-Type: application/vnd.schemaregistry.v1+json" \
    -d @"$SCRATCH/v3.json" "$SR/apis/ccompat/v7/subjects/$SUBJECT/versions")
  cat "$SCRATCH/v3-response.json"; echo
  echo "HTTP_STATUS:$v3_status"

  echo "--- (4) версии subject после эволюции ---"
  curl_r "$SR/apis/ccompat/v7/subjects/$SUBJECT/versions"; echo

  echo "--- assert ---"
  if [ "$v3_status" = "409" ] && grep -q '"error_code":409' "$SCRATCH/v3-response.json"; then
    echo "[assert] OK: v2 (совместимая) зарегистрирована, v3 (несовместимая) отклонена реестром (409)"
  else
    echo "[assert] FAIL: ожидался HTTP 409 при регистрации v3, получено $v3_status" >&2
    exit 1
  fi
}

# --- Часть 2: Kafka Connect — FileStream source+sink с SMT ---
scenario_connect() {
  echo "=================================================================="
  echo "=== Kafka Connect: FileStreamSource -> topic -> FileStreamSink (+ SMT) ==="
  echo "=================================================================="

  # ⚠️ Живая находка: FileStreamSourceConnector хранит позицию чтения файла
  # (offset) в internal-топике connect-offsets, ключ = имя коннектора + путь
  # файла — НЕЗАВИСИМО от жизненного цикла самого коннектора. Просто
  # DELETE + POST того же коннектора с тем же файлом того же размера НЕ
  # перечитывает файл заново (позиция "конец файла" уже сохранена с прошлого
  # прогона) — источник молча не эмитит ни одной записи. Симметрично, sink
  # хранит committed-офсет своей consumer-группы (имя группы = имя
  # коннектора) — если топик не пересоздать, на повторном прогоне sink
  # стартует с офсета "уже всё вычитано" и тоже не пишет новых строк. Первая
  # версия скрипта ловила это как n_in=10 n_out=0 (assert FAIL) — исправлено
  # явным сбросом offsets через REST (KIP-875) + пересозданием топика ниже.
  echo "--- сбрасываю offsets существующих коннекторов (KIP-875: STOP -> DELETE offsets -> DELETE connector) ---"
  # Безусловно (без предварительной проверки "существует ли коннектор") —
  # проверка через отдельный curl была источником ДРУГОЙ живой находки: на
  # этой машине REST-соединения к сервисам стенда изредка виснут ~20с и
  # рвутся (см. curl_r выше); если та самая "существует ли" проверка попадала
  # под такой обрыв, `if` трактовал коннектор как "не существует" и МОЛЧА
  # пропускал сброс offsets — коннектор потом пересоздавался с ДОСТОВЕРНО
  # старой позицией чтения файла. STOP/DELETE offsets/DELETE connector на
  # несуществующий коннектор просто возвращают ошибку — она безопасно
  # игнорируется `|| true`, дополнительная проверка существования не нужна.
  for name in ecosystem-file-source ecosystem-file-sink; do
    curl_r -X PUT "$CONNECT/connectors/$name/stop" >/dev/null 2>&1 || true
    curl_r -X DELETE "$CONNECT/connectors/$name/offsets" >/dev/null 2>&1 || true
    curl_r -X DELETE "$CONNECT/connectors/$name" >/dev/null 2>&1 || true
  done

  echo "--- проверяю, что offsets реально сброшены (иначе следующий деплой унаследует старую позицию файла) ---"
  # "offsets":[] (сброшены) И error_code 404 "not found" (коннектор уже
  # удалён DELETE-ом выше, а значит и его offset-состояния тоже нет) — ОБА
  # исхода означают "чистое состояние", не только первый.
  local reset_tries=0
  while [ "$reset_tries" -lt 10 ]; do
    src_off=$(curl_r "$CONNECT/connectors/ecosystem-file-source/offsets" 2>/dev/null || echo '{"offsets":["?"]}')
    if echo "$src_off" | grep -qE '"offsets":\[\]|"error_code":404'; then
      break
    fi
    reset_tries=$((reset_tries + 1))
    curl_r -X PUT "$CONNECT/connectors/ecosystem-file-source/stop" >/dev/null 2>&1 || true
    curl_r -X DELETE "$CONNECT/connectors/ecosystem-file-source/offsets" >/dev/null 2>&1 || true
    sleep 2
  done
  echo "--- source offsets после сброса: $src_off ---"
  if ! echo "$src_off" | grep -qE '"offsets":\[\]|"error_code":404'; then
    echo "[ops] ПРЕДУПРЕЖДЕНИЕ: не удалось подтвердить сброс source-offsets за $reset_tries попыток (см. offsets выше) — продолжаю, но перенос может НЕ произойти" >&2
  fi

  echo "--- пересоздаю топик ecosystem-connect-demo (идемпотентно, как в других стендах серии) ---"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
    --delete --topic ecosystem-connect-demo >/dev/null 2>&1 || true
  sleep 2
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
    --create --topic ecosystem-connect-demo --partitions 1 --replication-factor 3 >/dev/null 2>&1 || true

  mkdir -p ecosystem/connect-data/source ecosystem/connect-data/sink
  rm -f ecosystem/connect-data/sink/output.txt
  printf 'line-01 hello kafka connect\nline-02 second record\nline-03 third record\nline-04 fourth record\nline-05 fifth record\nline-06 sixth record\nline-07 seventh record\nline-08 eighth record\nline-09 ninth record\nline-10 tenth record\n' \
    > ecosystem/connect-data/source/input.txt
  local n_in
  n_in=$(wc -l < ecosystem/connect-data/source/input.txt | tr -d ' ')
  echo "--- вход: $n_in строк в ecosystem/connect-data/source/input.txt ---"

  echo "--- деплою source-коннектор (FileStreamSourceConnector + SMT hoist+insertOrigin) ---"
  curl_r -w "\nHTTP_STATUS:%{http_code}\n" -X POST -H "Content-Type: application/json" \
    -d @ecosystem/connect/file-source.json "$CONNECT/connectors"
  echo

  echo "--- деплою sink-коннектор (FileStreamSinkConnector) ---"
  curl_r -w "\nHTTP_STATUS:%{http_code}\n" -X POST -H "Content-Type: application/json" \
    -d @ecosystem/connect/file-sink.json "$CONNECT/connectors"
  echo

  echo "--- жду перенос данных ---"
  local tries=0 n_out=0
  while [ "$tries" -lt 20 ]; do
    n_out=$(docker exec kafka-cookbook-connect sh -c "wc -l < /data/sink/output.txt 2>/dev/null | tr -d ' '" || echo 0)
    [ "$n_out" = "$n_in" ] && break
    tries=$((tries + 1))
    sleep 1
  done

  echo "--- статус коннекторов (REST /connectors/{name}/status) ---"
  curl_r "$CONNECT/connectors/ecosystem-file-source/status"; echo
  curl_r "$CONNECT/connectors/ecosystem-file-sink/status"; echo

  echo "--- содержимое output.txt (после SMT: line + ingest_source) ---"
  docker exec kafka-cookbook-connect sh -c "cat /data/sink/output.txt"

  echo "--- счётчик: вход=$n_in выход=$n_out ---"
  local src_state sink_state
  src_state=$(curl_r "$CONNECT/connectors/ecosystem-file-source/status" | grep -o '"state":"[A-Z]*"' | head -1)
  sink_state=$(curl_r "$CONNECT/connectors/ecosystem-file-sink/status" | grep -o '"state":"[A-Z]*"' | head -1)

  echo "--- assert ---"
  if [ "$n_in" = "$n_out" ] && [ "$src_state" = '"state":"RUNNING"' ] && [ "$sink_state" = '"state":"RUNNING"' ]; then
    echo "[assert] OK: оба коннектора RUNNING, N in ($n_in) == N out ($n_out)"
  else
    echo "[assert] FAIL: source=$src_state sink=$sink_state n_in=$n_in n_out=$n_out" >&2
    exit 1
  fi
}

# --- Часть 3: сериализация — JSON vs Avro vs Protobuf (см. go/ecosystem/serialization) ---
scenario_serialization() {
  echo "=================================================================="
  echo "=== Сериализация: относительный размер JSON vs Avro vs Protobuf ==="
  echo "=================================================================="
  if [ ! -x "$GOBIN" ]; then
    echo "[ops] $GOBIN не найден — собираю..."
    docker run --rm -v "$(pwd)/go:/app" -w /app golang:1.25 sh -c "go build -o bin/ecosystem-serialization ./ecosystem/serialization"
  fi
  docker run --rm -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/ecosystem-serialization
}

# --- Cleanup: коннекторы + subject + рантайм-файлы (СЕРВИСЫ/КЛАСТЕР не трогает) ---
scenario_cleanup() {
  echo "=================================================================="
  echo "=== cleanup: коннекторы + Schema Registry subject + рантайм-файлы ==="
  echo "=================================================================="
  curl_r -X DELETE "$CONNECT/connectors/ecosystem-file-source" >/dev/null 2>&1 || true
  curl_r -X DELETE "$CONNECT/connectors/ecosystem-file-sink" >/dev/null 2>&1 || true
  curl_r -X DELETE "$SR/apis/ccompat/v7/subjects/$SUBJECT" >/dev/null 2>&1 || true
  curl_r -X DELETE "$SR/apis/ccompat/v7/subjects/$SUBJECT?permanent=true" >/dev/null 2>&1 || true
  rm -rf ecosystem/connect-data/source/* ecosystem/connect-data/sink/* "$SCRATCH"
  echo "--- список коннекторов после cleanup ---"
  curl_r "$CONNECT/connectors"; echo
  echo "--- список subjects после cleanup ---"
  curl_r "$SR/apis/ccompat/v7/subjects"; echo
  echo "[ops] schema-registry/kafka-connect контейнеры оставлены ЖИВЫМИ (для повторных прогонов)."
  echo "[ops] полный снос: docker compose -f compose/compose.yml -f compose/ecosystem.yml rm -sf schema-registry kafka-connect"
}

main() {
  local scenario="${1:-all}"
  case "$scenario" in
    schema-registry)
      up_ecosystem
      scenario_schema_registry
      ;;
    connect)
      up_ecosystem
      scenario_connect
      ;;
    serialization)
      scenario_serialization
      ;;
    cleanup)
      scenario_cleanup
      ;;
    all)
      up_ecosystem
      scenario_schema_registry
      scenario_connect
      scenario_serialization
      ;;
    *)
      echo "неизвестный сценарий: $scenario (schema-registry|connect|serialization|cleanup|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО ==="
}

main "$@"
