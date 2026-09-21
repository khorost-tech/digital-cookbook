#!/usr/bin/env bash
# build.sh — компиляция Java-части стенда «управление памятью».
#
# Портабельно: путь берётся из расположения скрипта, тулчейн — из окружения.
# На обычной машине (JDK 21 + Maven в PATH) достаточно `bash build.sh`.
#
# Переменные окружения (все опциональны):
#   JAVA_HOME   — JDK 21; если не задан, пробуем $HOME/jdk21, иначе java из PATH.
#   MVN         — путь к mvn; по умолчанию из PATH.
#   MAVEN_REPO  — локальный репозиторий Maven; по умолчанию стандартный (~/.m2).
#   BUILD_DIR   — куда собирать. По умолчанию — на месте. На Windows+WSL сборка
#                 Maven на drvfs (/mnt/*) повреждает target/*.class -> укажите
#                 нативную FS: BUILD_DIR="$HOME/osvbuild/mm-java" bash build.sh
set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
BUILD_DIR="${BUILD_DIR:-$SRC}"

if [ -z "${JAVA_HOME:-}" ] && [ -x "$HOME/jdk21/bin/java" ]; then
  export JAVA_HOME="$HOME/jdk21"
fi
JAVA="${JAVA_HOME:+$JAVA_HOME/bin/}java"
MVN="${MVN:-$(command -v mvn || echo mvn)}"
declare -a REPO_ARG=()
[ -n "${MAVEN_REPO:-}" ] && REPO_ARG=(-Dmaven.repo.local="$MAVEN_REPO")

echo "=== java -version ==="
"$JAVA" -version

if [ "$BUILD_DIR" != "$SRC" ]; then
  echo "=== copy sources to native FS: $BUILD_DIR ==="
  rm -rf "$BUILD_DIR"; mkdir -p "$(dirname "$BUILD_DIR")"; cp -r "$SRC" "$BUILD_DIR"
fi
cd "$BUILD_DIR"

echo "=== maven compile ==="
"$MVN" "${REPO_ARG[@]}" -q compile
echo "COMPILE_OK (build dir: $BUILD_DIR)"
