#!/usr/bin/env bash
# Собирает и прогоняет стенд data-access ВНУТРИ контейнера на сети compose
# (host-firewall может блокировать доступ хостового Java-процесса к localhost:5455 —
# как и в стендах WAL/db-indexes). Запуск из директории java-deep-dive/.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."   # -> java-deep-dive/

echo "=== 1. Postgres (docker compose) ==="
docker compose -f docker/compose.yml up -d postgres
sleep 8

NET="$(docker network ls --format '{{.Name}}' | grep -m1 '_default$' | grep -i docker || echo docker_default)"
echo "compose network: $NET"

echo "=== 2. Сборка data-access.jar (Docker Maven, JDK 25) ==="
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl data-access -am package -DskipTests

echo "=== 3. Прогон (в контейнере на сети $NET, DSN=postgres:5432) ==="
MSYS_NO_PATHCONV=1 docker run --rm --network "$NET" \
  -e JDBC_URL="jdbc:postgresql://postgres:5432/jdd" \
  -e JDBC_USER=jdd -e JDBC_PASSWORD=jdd \
  -v "$(pwd)/data-access/target:/app" eclipse-temurin:25-jdk \
  java -jar /app/data-access.jar

echo
echo "=== 4. Остановка Postgres ==="
docker compose -f docker/compose.yml down
