#!/usr/bin/env bash
# Прогоняет Workload под JFR-записью (встроена в JDK, ничего качать не нужно).
# Запись пишется в target/rec.jfr — гитигнорится (*.jfr), только для разбора локально.
#
# Требует: собранный target/profiling.jar (см. README) и Docker.
# Запуск из директории java-deep-dive/profiling/.
set -euo pipefail

DURATION="${1:-40}"
IMAGE="eclipse-temurin:25-jdk"
JAR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/target"

if [ ! -f "$JAR_DIR/profiling.jar" ]; then
  echo "profiling.jar не найден, соберите модуль сначала (см. README)." >&2
  exit 1
fi

echo "=== JFR recording, duration=${DURATION}s ==="
MSYS_NO_PATHCONV=1 docker run --rm -m 1g -v "$JAR_DIR:/app" "$IMAGE" \
  java -Xms128m -Xmx512m \
  -XX:StartFlightRecording=filename=/app/rec.jfr,settings=profile,dumponexit=true \
  -jar /app/profiling.jar "$DURATION"

echo
echo "=== jfr summary ==="
MSYS_NO_PATHCONV=1 docker run --rm -v "$JAR_DIR:/app" "$IMAGE" jfr summary /app/rec.jfr

echo
echo "Запись: $JAR_DIR/rec.jfr"
echo "Разбор аллокаций:  docker run --rm -v \"$JAR_DIR:/app\" $IMAGE jfr print --events jdk.ObjectAllocationSample /app/rec.jfr"
echo "Разбор GC-пауз:    docker run --rm -v \"$JAR_DIR:/app\" $IMAGE jfr print --events jdk.GCPhasePause /app/rec.jfr"
echo "Разбор hot-методов: docker run --rm -v \"$JAR_DIR:/app\" $IMAGE jfr print --events jdk.ExecutionSample /app/rec.jfr"
