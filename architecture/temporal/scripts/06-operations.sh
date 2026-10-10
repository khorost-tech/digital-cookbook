#!/usr/bin/env bash
#
# 06-operations.sh — артефакт к статье «Temporal в эксплуатации».
#
# Сюжет 1 (замер): schedule-to-start под ФИКСИРОВАННОЙ нагрузкой при
#   разных настройках воркера. Сервер при этом жив и не загружен — в том
#   и суть: симптом выглядит как «тормозит Temporal», а причина в ёмкости
#   воркера.
# Сюжет 2: тест с промоткой времени — воркфлоу «на сутки» проходит мгновенно.
#
# ИСТОЧНИК ЧИСЕЛ. schedule-to-start снимается из метрик SDK НАШЕГО
# воркера (job temporal-sdk-worker, цель ops-worker:8077), а не из метрик
# сервиса. В фикстурах у каждого числа проставляется source.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

STEPS="${STEPS:-60}"
COUNT="${COUNT:-4}"
RUN_ID="$(date +%s)"

cleanup() { docker rm -f ops-worker ops-worker-b ops-load >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

sdk_metric() {
    local v; v="$(prom_query "$1")"
    if [[ -z "${v}" ]]; then echo "НЕТ_РЯДА"; else echo "${v}"; fi
}

start_worker() {                  # start_worker <имя> <concurrency> <pollers> <очередь>
    go_start_in_net "$1" 06-operations worker \
        -concurrency "$2" -pollers "$3" -queue "$4" >/dev/null
    wait_for_log "$1" "concurrency=$2 pollers=$3" 300
}

measure() {                       # measure <метка> <воркеров> <concurrency> <pollers>
    local label="$1" workers="$2" conc="$3" poll="$4"
    local queue="ops-${label}-${RUN_ID}"
    local q="${queue//-/_}"

    docker rm -f ops-worker ops-worker-b >/dev/null 2>&1 || true
    start_worker ops-worker "${conc}" "${poll}" "${queue}"
    if (( workers > 1 )); then
        start_worker ops-worker-b "${conc}" "${poll}" "${queue}"
    fi
    sleep 10

    local line
    line="$(go_run_in_net ops-load 06-operations load \
        -queue "${queue}" -steps "${STEPS}" -count "${COUNT}" -label "${label}-${RUN_ID}" \
        | grep '^ЗАМЕР')"
    sleep 12

    # Среднее и максимум ожидания задачи в очереди — из метрик SDK.
    local s c mean mx
    s="$(sdk_metric "sum(temporal_activity_schedule_to_start_latency_seconds_sum{task_queue=\"${q}\"})")"
    c="$(sdk_metric "sum(temporal_activity_schedule_to_start_latency_seconds_count{task_queue=\"${q}\"})")"
    mx="$(sdk_metric "max(temporal_activity_schedule_to_start_latency_seconds{task_queue=\"${q}\"})")"
    if [[ "${s}" != "НЕТ_РЯДА" && "${c}" != "НЕТ_РЯДА" ]]; then
        mean="$(awk -v s="${s}" -v c="${c}" 'BEGIN { if (c>0) printf "%.3f", s/c; else print "НЕТ_ЗАДАЧ" }')"
    else
        mean="НЕТ_РЯДА"
    fi

    echo "${line} workers=${workers} concurrency=${conc} pollers=${poll} sched_to_start_mean_s=${mean} sched_to_start_max_s=${mx} source=sdk"
    docker rm -f ops-worker ops-worker-b >/dev/null 2>&1 || true
}

section "Сюжет 1 (замер): ёмкость воркера против schedule-to-start"
echo "  нагрузка фиксирована: ${COUNT} воркфлоу по ${STEPS} activity = $((COUNT*STEPS)) задач"
measure "tight"      1 2  2      # заведомо узко
measure "wide"       1 20 4      # тот же ОДИН воркер, больше параллелизма
measure "twoworkers" 2 20 4      # два воркера с той же настройкой

section "Проверка: узким местом был воркер, а не сервер"
echo "  если бы упирался сервер, расширение воркера не помогло бы —"
echo "  сравнение tight/wide отвечает именно на этот вопрос"
echo "  нагрузка на persistence в этот момент (метрика СЕРВИСА):"
svc="$(sdk_metric 'sum(rate(persistence_requests[1m]))')"
echo "    persistence_requests rate: ${svc}   source=service"

section "Сюжет 2: тест с промоткой времени"
docker run --rm --network "${NET}" \
    -v "${STAND_DIR}/go:/src" -v "${GOCACHE_VOL}:/gocache" -w /src \
    -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod \
    -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build \
    -e GOPROXY="https://go.khorost.tech,direct" \
    "${GO_IMAGE}" go test ./06-operations/... -run TestDayLongWorkflowSkipsTime -v 2>&1 \
    | grep -E 'PASS|FAIL|логические сутки' | sed 's/^/  /'
