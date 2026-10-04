#!/usr/bin/env bash
# Собирает оба образа (spring, quarkus; JVM-режим) и снимает startup/RSS по
# каждому. Методика идентична ../build-packaging/bench.sh: замер ВНУТРИ
# контейнера (bench-inside.sh), без публикации портов на хост -- иначе
# port-forwarder Docker Desktop на Windows добавляет к первому коннекту
# случайные 20-80 секунд и полностью топит startup-числа.
#
# startup (мс)  -- от старта процесса сервиса до первого 200 OK на GET /health
#                  (опрос из того же контейнера, bash /dev/tcp). У обеих сторон
#                  health вынесен на один и тот же путь /health (см.
#                  application.properties в spring/ и quarkus/).
# peak RSS (МБ) -- VmHWM процесса сервиса из /proc/$PID/status (та же метрика,
#                  что в ../concurrency и ../build-packaging).
#
# Требует: собранные target/ у обоих модулей (см. README.md -- сборка через
# Docker-образ Maven, хостового Maven нет).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

RUNS="${1:-5}"

MODES=(spring quarkus)
declare -A START_CMD=(
  [spring]='java -jar /app/app.jar'
  [quarkus]='java -jar /app/quarkus-run.jar'
)

echo "=== docker build (spring, quarkus) ===" >&2
for mode in "${MODES[@]}"; do
  echo "--- ${mode} ---" >&2
  docker build -t "jdd-svq-${mode}" "${mode}" >&2
done

measure_one() {
  local mode="$1"
  # MSYS_NO_PATHCONV=1 обязателен на git-bash: иначе '/app/...' в START_CMD
  # манглится в windows-путь ещё на хосте.
  MSYS_NO_PATHCONV=1 docker run --rm -i --entrypoint bash "jdd-svq-${mode}" -s "${START_CMD[$mode]}" < bench-inside.sh 2>/dev/null
}

echo >&2
echo "=== прогоны (${RUNS} на режим) ===" >&2
declare -A ALL_STARTUP ALL_RSS
for mode in "${MODES[@]}"; do
  ALL_STARTUP[$mode]=""
  ALL_RSS[$mode]=""
  for run in $(seq 1 "$RUNS"); do
    result=$(measure_one "$mode" || true)
    startup_ms=$(echo "$result" | sed -n 's/.*startup_ms=\([^ ]*\).*/\1/p')
    rss_kb=$(echo "$result" | sed -n 's/.*peak_rss_kb=\([0-9]*\).*/\1/p')
    if [ "${startup_ms:-FAIL}" = "FAIL" ] || [ -z "${startup_ms:-}" ]; then
      echo "${mode} run ${run}: FAILED (raw: '$result')" >&2
      continue
    fi
    rss_mb=$(awk -v kb="${rss_kb:-0}" 'BEGIN{printf "%.1f", kb/1024}')
    echo "${mode} run ${run}: startup_ms=${startup_ms} peak_rss_kb=${rss_kb} (~${rss_mb} MB)" >&2
    ALL_STARTUP[$mode]="${ALL_STARTUP[$mode]} ${startup_ms}"
    ALL_RSS[$mode]="${ALL_RSS[$mode]} ${rss_mb}"
  done
done

echo >&2
echo "=== размер образов ===" >&2
declare -A SIZE_MB
for mode in "${MODES[@]}"; do
  size_bytes=$(docker image inspect "jdd-svq-${mode}" --format='{{.Size}}')
  size_mb=$(awk -v b="$size_bytes" 'BEGIN{printf "%.1f", b/1024/1024}')
  SIZE_MB[$mode]="$size_mb"
  echo "jdd-svq-${mode}: ${size_mb} MB (${size_bytes} bytes)" >&2
done

echo
echo "| Фреймворк | startup (мс, все прогоны) | peak RSS (МБ, все прогоны) | размер образа (МБ) |"
echo "|---|---|---|---|"
for mode in "${MODES[@]}"; do
  echo "| ${mode} |${ALL_STARTUP[$mode]} |${ALL_RSS[$mode]} | ${SIZE_MB[$mode]} |"
done
