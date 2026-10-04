#!/usr/bin/env bash
# Прогоняет корутин-бенчмарк (2 повтора) в ОТДЕЛЬНОМ контейнере/процессе JVM
# на прогон — тот же приём, что и в `concurrency/run-all.sh`,
# для честного peak RSS (VmHWM одного прогона не смешивается с
# другим внутри общего процесса).
#
# Требует: собранный build/libs/kotlin-backend-0.1.0-all.jar (см. README —
# `gradle build shadowJar` через Docker) и Docker.
# Запуск из директории java-deep-dive/kotlin/.
set -euo pipefail

N="${1:-10000}"
SLEEP_MS="${2:-100}"
IMAGE="eclipse-temurin:25-jdk"
JAR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build/libs"
JAR="kotlin-backend-0.1.0-all.jar"

if [ ! -f "$JAR_DIR/$JAR" ]; then
  echo "$JAR не найден, соберите модуль сначала:" >&2
  echo '  MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD":/app -v gradle-cache:/home/gradle/.gradle -w /app gradle:9-jdk25 gradle build shadowJar' >&2
  exit 1
fi

for run in 1 2; do
  echo "=== coroutines, run ${run} (n=${N}, sleep=${SLEEP_MS}ms) ==="
  docker run --rm -m 4g -v "$JAR_DIR:/app" "$IMAGE" \
    java -Xms64m -Xmx1g -cp "/app/$JAR" tech.khorost.kotlin.bench.CoroutineBenchKt "$N" "$SLEEP_MS"
  echo
done
