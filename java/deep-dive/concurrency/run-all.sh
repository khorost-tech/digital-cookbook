#!/usr/bin/env bash
# Прогоняет все 4 режима стенда, каждый — в ОТДЕЛЬНОМ контейнере/процессе JVM,
# чтобы peak RSS (VmHWM) одного режима не смешивался с другим.
#
# Требует: собранный target/concurrency.jar (см. build ниже) и Docker.
# Запуск из директории java-deep-dive/concurrency/.
set -euo pipefail

N="${1:-10000}"
SLEEP_MS="${2:-100}"
IMAGE="eclipse-temurin:25-jdk"
JAR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/target"

if [ ! -f "$JAR_DIR/concurrency.jar" ]; then
  echo "concurrency.jar не найден, соберите модуль сначала:" >&2
  echo '  cd .. && MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 mvn -q -pl concurrency -am package' >&2
  exit 1
fi

for mode in vt platform platform-large reactor; do
  echo "=== ${mode} (n=${N}, sleep=${SLEEP_MS}ms) ==="
  docker run --rm -m 4g -v "$JAR_DIR:/app" "$IMAGE" \
    java -Xms64m -Xmx1g -jar /app/concurrency.jar "$mode" "$N" "$SLEEP_MS"
  echo
done
