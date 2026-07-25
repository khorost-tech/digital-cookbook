#!/usr/bin/env bash
# Собирает все 5 образов build-packaging и снимает startup/RSS/размер по каждому.
# Каждый режим — ОТДЕЛЬНЫЙ контейнер (как в concurrency/run-all.sh: peak RSS
# одного режима не должен смешиваться с другим).
#
# ВАЖНО про методику startup/RSS: замер выполняется ВНУТРИ контейнера
# (bench-inside.sh) без публикации портов на хост. Причина — port-forwarder
# Docker Desktop на Windows добавляет к первому коннекту случайные 20–80 секунд,
# что полностью топит startup-числа (JVM стартует за ~сотни мс, а измерялось бы
# «87 c»). Перенос опроса /health и чтения VmHWM внутрь контейнера убирает этот
# артефакт хоста. Размер образа меряется снаружи (docker image inspect) — на него
# port-forwarder не влияет.
#
# startup (мс)  — от старта процесса сервиса до первого 200 OK на GET /health
#                 (опрос из того же контейнера, bash /dev/tcp).
# peak RSS (МБ) — VmHWM процесса сервиса из /proc/$PID/status (историчный пик,
#                 та же метрика, что в ../concurrency).
# размер (МБ)   — docker image inspect --format='{{.Size}}' (несжатый размер
#                 образа с базовым слоём) — единая метрика для всех 5.
#
# Запуск из директории build-packaging/. Требует: Docker, собранный target/
# (см. README для команды mvn package через Docker-образ Maven).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

RUNS="${1:-5}"

MODES=(fat layered appcds native jlink)
declare -A DOCKERFILE=(
  [fat]=Dockerfile.fat
  [layered]=Dockerfile.layered
  [appcds]=Dockerfile.appcds
  [native]=Dockerfile.native
  [jlink]=Dockerfile.jlink
)
# Команда запуска сервиса внутри контейнера для каждого режима (совпадает с CMD
# соответствующего Dockerfile) — передаётся в bench-inside.sh и там eval'ится.
declare -A START_CMD=(
  [fat]='java -jar /app/app.jar'
  [layered]='java -cp /app/classes tech.khorost.buildpackaging.app.App'
  [appcds]='java -XX:SharedArchiveFile=/app/app.jsa -jar /app/app.jar'
  [native]='/app/app'
  [jlink]='/opt/customjre/bin/java -jar /app/app.jar'
)

echo "=== docker build (5 режимов) ===" >&2
for mode in "${MODES[@]}"; do
  echo "--- ${mode} (${DOCKERFILE[$mode]}) ---" >&2
  docker build -f "${DOCKERFILE[$mode]}" -t "jdd-bp-${mode}" . >&2
done

measure_one() {
  local mode="$1"
  # bench-inside.sh подаётся на stdin (bash -s), первый позиционный аргумент —
  # строка команды запуска. --entrypoint bash, чтобы работало и для native
  # (там базовый образ debian:bookworm-slim без java в ENTRYPOINT).
  # MSYS_NO_PATHCONV=1 обязателен на git-bash: иначе '/app/app' в аргументе
  # манглится в windows-путь ('C:/Program Files/Git/app/app') ещё на хосте.
  MSYS_NO_PATHCONV=1 docker run --rm -i --entrypoint bash "jdd-bp-${mode}" -s "${START_CMD[$mode]}" < bench-inside.sh 2>/dev/null
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
  size_bytes=$(docker image inspect "jdd-bp-${mode}" --format='{{.Size}}')
  size_mb=$(awk -v b="$size_bytes" 'BEGIN{printf "%.1f", b/1024/1024}')
  SIZE_MB[$mode]="$size_mb"
  echo "jdd-bp-${mode}: ${size_mb} MB (${size_bytes} bytes)" >&2
done

echo
echo "| Режим | startup (мс, все прогоны) | peak RSS (МБ, все прогоны) | размер образа (МБ) |"
echo "|---|---|---|---|"
for mode in "${MODES[@]}"; do
  echo "| ${mode} |${ALL_STARTUP[$mode]} |${ALL_RSS[$mode]} | ${SIZE_MB[$mode]} |"
done
