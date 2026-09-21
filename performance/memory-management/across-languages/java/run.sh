#!/usr/bin/env bash
# run.sh — прогон трёх опор Java-части стенда. Запускать ПОСЛЕ build.sh.
#
# Те же переменные окружения, что у build.sh (JAVA_HOME/MAVEN_REPO/BUILD_DIR).
# GC-логи опоры 3 копируются обратно в директорию стенда.
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="${BUILD_DIR:-$SRC}"

if [ -z "${JAVA_HOME:-}" ] && [ -x "$HOME/jdk21/bin/java" ]; then
  export JAVA_HOME="$HOME/jdk21"
fi
JAVA="${JAVA_HOME:+$JAVA_HOME/bin/}java"
MAVEN_REPO="${MAVEN_REPO:-$HOME/.m2}"

# JOL jar ищем в локальном репозитории Maven ПО КОНКРЕТНОЙ версии (из pom.xml),
# а не первый попавшийся — иначе при нескольких версиях можно взять не ту.
JOL_VERSION="0.17"
JOL="$(find "$MAVEN_REPO" -path "*/jol-core/${JOL_VERSION}/jol-core-${JOL_VERSION}.jar" 2>/dev/null | head -1 || true)"
if [ -z "$JOL" ]; then
  echo "!!! jol-core-${JOL_VERSION}.jar не найден в $MAVEN_REPO — сначала запустите build.sh (Maven скачает JOL)" >&2
  exit 1
fi
CP="$BUILD_DIR/target/classes:$JOL"
cd "$BUILD_DIR"

echo "### java -version"; "$JAVA" -version 2>&1
echo; echo "### JOL: $JOL"

echo; echo "### Опора 1 — Cycle (трассирующий GC собирает цикл)"
"$JAVA" -Xmx2g -cp "$CP" tech.khorost.mm.Cycle

echo; echo "### Опора 2 — Layout (JOL: раскладка объекта + autoboxing)"
"$JAVA" -cp "$CP" tech.khorost.mm.Layout

# Опора 3. ПРИМЕЧАНИЕ: в JDK 21 `-XX:+UseZGC` включает НЕпоколенческий ZGC;
# поколенческий ZGC — отдельным флагом `-XX:+ZGenerational` (в JDK 21 это preview,
# по умолчанию с JDK 23). Здесь сравниваем поколенческий G1 с непоколенческим ZGC.
echo; echo "### Опора 3 — GcWorkload прогон 1: G1 (поколенческий)"
rm -f gcpauses-g1.txt gcpauses-zgc.txt
"$JAVA" -Xmx512m -XX:+UseG1GC -Xlog:gc,gc+phases:file=gcpauses-g1.txt \
    -cp "$CP" tech.khorost.mm.GcWorkload
echo "--- pause lines (G1) ---"; grep -i "Pause" gcpauses-g1.txt | head -25 || true
echo "--- G1 pause count: $(grep -ci 'Pause' gcpauses-g1.txt) ---"

echo; echo "### Опора 3 — GcWorkload прогон 2: ZGC (непоколенческий, JDK 21)"
"$JAVA" -Xmx512m -XX:+UseZGC -Xlog:gc,gc+phases:file=gcpauses-zgc.txt \
    -cp "$CP" tech.khorost.mm.GcWorkload
echo "--- pause lines (ZGC) ---"; grep -i "Pause" gcpauses-zgc.txt | head -25 || true
echo "--- ZGC pause count: $(grep -ci 'Pause' gcpauses-zgc.txt) ---"

# Синхронизируем GC-логи обратно в директорию стенда (если собирали на нативной FS).
if [ "$BUILD_DIR" != "$SRC" ]; then
  cp -f gcpauses-g1.txt gcpauses-zgc.txt "$SRC/"
fi
echo "=== done run.sh ==="
