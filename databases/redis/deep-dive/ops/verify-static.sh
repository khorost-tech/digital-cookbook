#!/usr/bin/env bash
# Финальная версия (Стенд #7, Task 8): добавлен шаг Java-реактора рядом с
# Go-модулями и compose. Java собирается ДОКЕРИЗОВАННЫМ Maven (образ
# maven:3.9-eclipse-temurin-21, matches maven.compiler.release=21 в
# java/pom.xml), тем же приёмом, что и в соседних стендах databases/mongodb,
# databases/clickhouse — портативно (работает и на self-hosted runner, и
# локально), без зависимости от того, стоит ли mvn на хосте. НЕ через WSL —
# нативная сборка mvn там даёт побитый target/ на /mnt (drvfs), и в любом
# случае WSL — деталь ЭТОЙ машины, а не переносимый способ проверки.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
export MSYS_NO_PATHCONV=1

echo "[verify-static] gofmt/govet по всем Go-модулям..."
for mod in dataset structures eventloop persistence topology memory-eviction streams-lua ops-stand/go; do
  [ -d "$mod" ] || continue
  (cd "$mod" && gofmt -l . | (! grep .) && go vet ./...)
done

echo "[verify-static] Java-реактор (mvn -q compile, докеризовано)..."
if [ -f java/pom.xml ]; then
  mkdir -p .m2cache
  docker run --rm \
    -v "$HERE:/app" -v "$HERE/.m2cache:/root/.m2" \
    -w /app/java maven:3.9-eclipse-temurin-21 \
    mvn -q -Dmaven.repo.local=/root/.m2 compile
else
  echo "[verify-static] нет java/pom.xml — шаг пропущен"
fi

echo "[verify-static] compose синтаксис..."
docker compose -f compose/base.yml config >/dev/null
[ -f compose/cluster.yml ] && docker compose -f compose/cluster.yml config >/dev/null
[ -f compose/sentinel.yml ] && docker compose -f compose/sentinel.yml config >/dev/null
echo "[verify-static] OK"
