#!/usr/bin/env bash
# inspect-segments.sh — оркестрирует сценарии стенда #4 ("хранение: retention,
# log compaction, компрессия"), которые требуют инспекции РЕАЛЬНЫХ файлов
# сегментов на брокере (kafka-log-dirs.sh, ls) и изменения dynamic
# broker-конфигов МЕЖДУ вызовами клиента (у Go/Java клиента внутри
# контейнера нет доступа к docker socket и нет способа увидеть файлы
# сегментов напрямую) — см. ../go/storage и ../java/storage.
#
# Аналог по духу ops/broker-kill.sh (стенд #3): та же "фазовая" схема,
# та же -client=go|java диспетчеризация.
#
# ⚠️ ВАЖНО про dynamic broker-конфиги (проверено живьём): kafka-configs.sh
# --entity-type brokers --entity-default хранит значения в METADATA LOG
# кластера (KRaft __cluster_metadata), а НЕ в server.properties контейнера.
# У этого стенда (как и у всего compose/compose.yml) НЕТ персистентного
# volume под /tmp/kafka-logs — при любом `docker compose up -d`, который
# ПЕРЕСОЗДАЁТ (не просто рестартует) контейнеры, весь storage форматируется
# заново, и любые dynamic-конфиги, выставленные ЗДЕСЬ РАНЕЕ, ТЕРЯЮТСЯ. Этот
# скрипт поэтому переустанавливает нужные dynamic-конфиги (log.cleaner.
# backoff.ms) в начале КАЖДОГО прогона сценария compaction — дёшево,
# идемпотентно, не полагается на состояние предыдущих запусков.
#
# ⚠️ log.retention.check.interval.ms — НЕ dynamic (проверено живьём:
# kafka-configs.sh алтер падает с "Cannot update these configs dynamically").
# Задан статически в ../compose/compose.yml
# (KAFKA_LOG_RETENTION_CHECK_INTERVAL_MS=2000) — если кластер поднят БЕЗ
# этого значения (старый compose без переменной), retention-сценарий будет
# ждать РЕАЛЬНЫЙ дефолт брокера (300000мс = 5 мин) вместо секунд.
#
# Требует: кластер поднят (docker compose -f ../compose/compose.yml up -d),
# собранный Go-бинарь ../go/bin/storage или java -jar (собирается сам при
# первом запуске, как в broker-kill.sh).
#
# Запуск: bash ops/inspect-segments.sh [-client=go|java] <scenario>
#   -client: go (по умолчанию) | java
#   scenario: retention | compaction | compression | tiered | all (по умолчанию)

set -euo pipefail
export MSYS_NO_PATHCONV=1        # Git Bash on Windows: не переписывать unix-style пути в docker-аргументах
cd "$(dirname "$0")/.."          # kafka/
GOBIN="go/bin/storage"
JAVA_JAR="java/storage/target/storage.jar"
CLIENT="${CLIENT:-go}"
LOGDIR="/tmp/kafka-logs"         # log.dirs внутри контейнеров брокера (проверено живьём)

require_binary() {
  case "$CLIENT" in
    go)
      if [ ! -x "$GOBIN" ]; then
        echo "[ops] $GOBIN не найден — собираю..."
        docker run --rm -v "$(pwd)/go:/app" -w /app golang:1.25 sh -c "go build -o bin/storage ./storage"
      fi
      ;;
    java)
      if [ ! -f "$JAVA_JAR" ]; then
        echo "[ops] $JAVA_JAR не найден — собираю..."
        docker run --rm -v "$(pwd)/java:/app" -w /app maven:3.9-eclipse-temurin-25 \
          sh -c "mvn -q -pl storage -am package -DskipTests"
      fi
      ;;
  esac
}

# client_run — запускает одну фазу выбранного клиента; см. broker-kill.sh —
# идентичный паттерн трансляции флагов "-key=value" (Go) <-> "--key=value" (Java).
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
        java -jar /app/storage/target/storage.jar "${jargs[@]}"
      ;;
    *)
      docker run --rm --network kafka-cookbook-net -v "$(pwd)/go/bin:/app" -w /app golang:1.25 /app/storage "$@"
      ;;
  esac
}

# set_cleaner_backoff — переустанавливает log.cleaner.backoff.ms (dynamic,
# cluster-wide default) на всех брокерах. 1000мс вместо дефолтных 15000мс —
# иначе живая демонстрация компакции растягивается на минуты. НЕ влияет на
# корректность (только на скорость обнаружения "грязных" данных cleaner'ом),
# так что безопасно для остальных стендов.
set_cleaner_backoff() {
  local ms="$1"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
    --entity-type brokers --entity-default --alter --add-config log.cleaner.backoff.ms="$ms" >/dev/null
  echo "[ops] log.cleaner.backoff.ms=$ms (dynamic, cluster-wide default)"
}

# segment_files — печатает *.log/*.log.deleted файлы партиции-0 на ОДНОМ
# брокере (leader не всегда один и тот же между прогонами — берём broker 1,
# он держит полную реплику при RF=3 независимо от того, лидер он или нет).
segment_files() {
  local topic="$1" label="$2"
  echo "--- $label: сегменты $topic-0 (broker 1, $LOGDIR) ---"
  docker exec kafka-cookbook-1 sh -c "ls -la $LOGDIR/$topic-0/ 2>/dev/null | grep -E '\\.log(\\.deleted)?\$' || echo '(партиция ещё не создана или директория пуста)'"
}

# topic_size — суммарный размер данных партиции-0 через kafka-log-dirs.sh
# (официальный клиентский способ узнать физический размер на диске, без
# прямого docker exec ls -la — используется для compress-сценария, где нужно
# точное байтовое число, а не список файлов).
topic_size_bytes() {
  local topic="$1"
  # </dev/null — без этого на Windows/Git Bash docker exec иногда дублирует
  # вывод команды напрямую в терминал ПОМИМО захвата через $(...) (замечено
  # живьём: строка с размером печаталась 2-3 раза подряд в разных форматах).
  # Итоговое ЗНАЧЕНИЕ было корректным в каждом случае — это чисто
  # косметический артефакт наследования stdin/tty в pipe-цепочке, не ошибка
  # в данных.
  docker exec </dev/null kafka-cookbook-1 /opt/kafka/bin/kafka-log-dirs.sh --bootstrap-server localhost:9092 \
    --describe --topic-list "$topic" 2>/dev/null | tail -1 \
    | grep -oE "\"partition\":\"$topic-0\",\"size\":[0-9]+" | grep -oE '[0-9]+$'
}

# --- Сценарий 1: retention (время/размер) — записать, показать удаление
# старых сегментов, earliest offset сдвигается вперёд. ---
scenario_retention() {
  echo "=================================================================="
  echo "=== retention: малый retention.ms, roll по segment.bytes, ждём чистку ==="
  echo "=================================================================="
  local topic=demo-storage-retention

  client_run -scenario=retention-setup -topic="$topic" -retention-ms=8000 -segment-bytes=1048576
  # 600 записей x ~5000 байт (без компрессии, см. producer.go newSyncProducer)
  # = ~3МБ логических данных при segment.bytes=1МБ -> около 3 закрытых
  # сегментов, есть что реально удалять.
  client_run -scenario=retention-produce -topic="$topic" -n=600 -pad-bytes=5000
  client_run -scenario=offsets -topic="$topic" -label=до

  segment_files "$topic" "ДО ожидания retention"

  echo "--- жду retention.ms (8с) + log.retention.check.interval.ms (2с, статический — см. compose.yml) + запас ---"
  sleep 14

  segment_files "$topic" "ПОСЛЕ ожидания retention"
  client_run -scenario=offsets -topic="$topic" -label=после -min-earliest-gt=0
}

# --- Сценарий 2: log compaction (ЯДРО стенда) — писать по ключам с
# обновлениями + tombstone, форсировать компакцию, показать: 1 значение на
# живой ключ (последнее), tombstone-ключ отсутствует полностью. ---
#
# ⚠️ Живьём обнаруженный факт (см. README/content-notes): физическое удаление
# tombstone-маркера требует ВТОРОГО прохода log cleaner'а над сегментом,
# который его содержит (первый проход схлопывает версии живых ключей, но
# ОСТАВЛЯЕТ tombstone — предохранитель Kafka от гонки с отстающими
# консьюмерами, см. KAFKA-3137). Проход 2 сам не запускается без новых
# "грязных" данных у лога — нужен ЕЩЁ один roll сегмента. Поэтому здесь ДВА
# раунда filler (filler-start продолжает нумерацию ключей между раундами —
# иначе второй раунд просто перезаписал бы те же filler-ключи первого).
scenario_compaction() {
  echo "=================================================================="
  echo "=== compaction: обновления по ключам + tombstone, 2 прохода cleaner'а ==="
  echo "=================================================================="
  local topic=demo-storage-compact

  set_cleaner_backoff 1000

  client_run -scenario=compact-setup -topic="$topic" -segment-bytes=1048576
  client_run -scenario=compact-produce-business -topic="$topic" -pad-bytes=5000 -rounds=4

  echo "--- consume ДО: сегмент с бизнес-записями ещё АКТИВЕН (roll не было) — cleaner физически не может его тронуть ---"
  client_run -scenario=compact-consume -topic="$topic" -label=до-компакции -idle=3s

  echo "--- filler раунд 1: форсирует roll -> сегмент с бизнес-записями закрывается, становится виден cleaner'у ---"
  client_run -scenario=compact-produce-filler -topic="$topic" -pad-bytes=5000 -filler-n=250 -filler-start=0
  segment_files "$topic" "после filler #1 (roll)"

  echo "--- жду проход 1 (схлопывает версии живых ключей, tombstone пока ОСТАЁТСЯ) ---"
  sleep 6
  client_run -scenario=compact-consume -topic="$topic" -label=после-прохода-1 -idle=3s

  echo "--- filler раунд 2 (нумерация продолжается с 250): форсирует ЕЩЁ один roll -> проход 2 -> физическое удаление tombstone ---"
  client_run -scenario=compact-produce-filler -topic="$topic" -pad-bytes=5000 -filler-n=250 -filler-start=250
  segment_files "$topic" "после filler #2 (roll)"

  echo "--- жду проход 2 ---"
  sleep 8
  echo "--- consume ПОСЛЕ + assert: 1 значение/живой ключ (последний раунд), tombstone-ключи отсутствуют ---"
  client_run -scenario=compact-consume -topic="$topic" -label=после-компакции -idle=3s -assert
}

# --- Сценарий 3: компрессия — none/gzip/lz4/zstd на одних данных,
# относительный размер лога (характерный прогон, host-зависимый по абсолюту). ---
scenario_compression() {
  echo "=================================================================="
  echo "=== компрессия: none vs gzip vs lz4 vs zstd, одни и те же данные ==="
  echo "=================================================================="
  local codecs="none gzip lz4 zstd"
  local topics=""
  for c in $codecs; do
    local t="demo-storage-compress-$c"
    topics="$topics,$t"
    client_run -scenario=compress-setup -topic="$t" -codec="$c"
  done
  for c in $codecs; do
    local t="demo-storage-compress-$c"
    client_run -scenario=compress-produce -topic="$t" -codec="$c" -n=3000 -pad-bytes=300
  done
  echo "--- размеры на диске (broker 1, партиция 0, байт) ---"
  for c in $codecs; do
    local t="demo-storage-compress-$c"
    local sz
    sz=$(topic_size_bytes "$t")
    echo "  $c: $sz байт"
  done
  echo "[info] характерный прогон — абсолютные числа host/данные-зависимы; сравнивать МЕЖДУ кодеками на этом прогоне, не как universal benchmark"
}

# --- Сценарий 4: tiered storage — попытка, честный статус (см.
# README/content-notes за разбор). ---
scenario_tiered() {
  echo "=================================================================="
  echo "=== tiered storage: попытка (см. README за честный итог) ==="
  echo "=================================================================="
  echo "--- remote.log.storage.system.enable (текущее значение брокера) ---"
  docker exec kafka-cookbook-1 /opt/kafka/bin/kafka-configs.sh --bootstrap-server localhost:9092 \
    --entity-type brokers --entity-name 1 --describe --all 2>&1 | grep -i "remote.log.storage.system.enable" || echo "(конфиг не найден в --describe --all)"
  echo "--- поиск классов RemoteStorageManager/RemoteLogMetadataManager в образе (bundled-плагин для локального tiered storage) ---"
  docker exec kafka-cookbook-1 sh -c 'find /opt/kafka/libs -iname "*tiered*" -o -iname "*remote-storage*" 2>/dev/null' || true
  echo "[info] см. README.md — честный итог попытки (демонстрировано / не демонстрировано и почему)"
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
    retention) scenario_retention ;;
    compaction) scenario_compaction ;;
    compression) scenario_compression ;;
    tiered) scenario_tiered ;;
    all)
      scenario_retention
      scenario_compaction
      scenario_compression
      scenario_tiered
      ;;
    *)
      echo "неизвестный сценарий: $scenario (retention|compaction|compression|tiered|all)" >&2
      exit 1
      ;;
  esac
  echo "=== ГОТОВО ==="
}

main "$@"
