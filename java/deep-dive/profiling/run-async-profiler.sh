#!/usr/bin/env bash
# Прогоняет Workload под async-profiler (нативный агент, качается отдельно
# от JDK) — снимает CPU flame и alloc-профиль в collapsed-формате.
#
# async-profiler качается ОДИН РАЗ через HTTP-прокси s10.khorost.tech:3128
# (крупная внешняя загрузка) в .ap-cache/ — эта директория в .gitignore,
# тарболл/бинарник в репозиторий не коммитятся.
#
# Требует: собранный target/profiling.jar (см. README) и Docker.
# Запуск из директории java-deep-dive/profiling/.
set -euo pipefail

AP_VERSION="4.4"
AP_ARCH="linux-x64"
AP_TARBALL="async-profiler-${AP_VERSION}-${AP_ARCH}.tar.gz"
AP_URL="https://github.com/async-profiler/async-profiler/releases/download/v${AP_VERSION}/${AP_TARBALL}"

DURATION="${1:-40}"
IMAGE="eclipse-temurin:25-jdk"
MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JAR_DIR="$MODULE_DIR/target"
AP_CACHE="$MODULE_DIR/.ap-cache"

if [ ! -f "$JAR_DIR/profiling.jar" ]; then
  echo "profiling.jar не найден, соберите модуль сначала (см. README)." >&2
  exit 1
fi

mkdir -p "$AP_CACHE"

if [ ! -f "$AP_CACHE/$AP_TARBALL" ]; then
  echo "=== скачиваю async-profiler ${AP_VERSION} (${AP_ARCH}) через прокси ==="
  curl -sL --proxy "http://s10.khorost.tech:3128" -o "$AP_CACHE/$AP_TARBALL" "$AP_URL"
fi

if [ ! -d "$AP_CACHE/async-profiler-${AP_VERSION}-${AP_ARCH}" ]; then
  echo "=== распаковываю async-profiler (внутри контейнера, Linux-бинарник) ==="
  MSYS_NO_PATHCONV=1 docker run --rm -v "$AP_CACHE:/ap" "$IMAGE" \
    bash -c "cd /ap && tar xzf $AP_TARBALL"
fi

AP_HOME="/ap/async-profiler-${AP_VERSION}-${AP_ARCH}"

echo "=== CPU-профиль (collapsed), duration=${DURATION}s ==="
MSYS_NO_PATHCONV=1 docker run --rm -m 1g \
  -v "$JAR_DIR:/app" -v "$AP_CACHE:/ap" "$IMAGE" \
  java -Xms128m -Xmx512m \
  -agentpath:${AP_HOME}/lib/libasyncProfiler.so=start,event=cpu,collapsed,file=/app/cpu.collapsed \
  -jar /app/profiling.jar "$DURATION"

echo
echo "=== alloc-профиль (collapsed), duration=${DURATION}s ==="
MSYS_NO_PATHCONV=1 docker run --rm -m 1g \
  -v "$JAR_DIR:/app" -v "$AP_CACHE:/ap" "$IMAGE" \
  java -Xms128m -Xmx512m \
  -agentpath:${AP_HOME}/lib/libasyncProfiler.so=start,event=alloc,collapsed,file=/app/alloc.collapsed \
  -jar /app/profiling.jar "$DURATION"

echo
echo "Top CPU-фреймы:"
sort -t' ' -k2 -rn "$JAR_DIR/cpu.collapsed" | head -10
echo
echo "Top alloc-фреймы:"
sort -t' ' -k2 -rn "$JAR_DIR/alloc.collapsed" | head -10
