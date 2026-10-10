#!/usr/bin/env bash
#
# 02-determinism.sh — артефакт к статье «Workflow и детерминизм».
#
# Сюжет 1: воспроизводим non-determinism error на воркфлоу с time.Now()
#          и rand внутри тела.
# Сюжет 2: то же на детерминированных примитивах — replay чист.
# Сюжет 3 (замер): во что обходится replay при разной длине истории.
#          Меряется чистое проигрывание по файлу, без сервера.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

HIST_DIR="${STAND_DIR}/go/02-determinism/histories"
mkdir -p "${HIST_DIR}"

cleanup() { docker rm -f d2-worker d2-run >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

# Запуск профиля с примонтированным каталогом историй на запись.
d2_run() {
    local sub="$1"; shift
    docker run --rm --name d2-run --network "${NET}" \
        -v "${STAND_DIR}/go:/src" \
        -v "${GOCACHE_VOL}:/gocache" \
        -v "${HIST_DIR}:/histories" \
        -w /src \
        -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod \
        -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build \
        -e GOPROXY="https://go.khorost.tech,direct" \
        "${GO_IMAGE}" go run ./02-determinism "${sub}" -address "${TEMPORAL_ADDR}" "$@"
}

# Воркер поднимается с DEMO_BRANCH=long: именно это внешнее состояние
# сломанный воркфлоу читает в теле, и именно оно попадёт в записанную
# историю. Replay-тест потом проигрывает ту же историю с другим
# значением — расхождение воспроизводится всегда, а не через раз.
docker run -d --name d2-worker --network "${NET}" \
    -v "${STAND_DIR}/go:/src" -v "${GOCACHE_VOL}:/gocache" -w /src \
    -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod \
    -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build \
    -e GOPROXY="https://go.khorost.tech,direct" \
    -e DEMO_BRANCH=long \
    "${GO_IMAGE}" go run ./02-determinism worker -address "${TEMPORAL_ADDR}" >/dev/null
wait_for_log d2-worker "зарегистрированы broken/fixed/long" 300

section "Сюжет 1: сломанный воркфлоу — исполнение и выгрузка истории"
d2_run run -kind broken -workflow broken-demo
d2_run dump -workflow broken-demo

section "Сюжет 2: исправленный воркфлоу"
d2_run run -kind fixed -workflow fixed-demo
d2_run dump -workflow fixed-demo

section "Сюжеты 1+2: replay обеих историй тестом"
docker run --rm --network "${NET}" \
    -v "${STAND_DIR}/go:/src" -v "${GOCACHE_VOL}:/gocache" -w /src \
    -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod \
    -e GOMODCACHE=/gocache/mod -e GOCACHE=/gocache/build \
    -e GOPROXY="https://go.khorost.tech,direct" \
    "${GO_IMAGE}" go test ./02-determinism/... -run 'Replay' -v 2>&1 | sed 's/^/  /'

section "Сюжет 3 (замер): цена replay от длины истории"
for steps in 5 50 500 2500; do
    wf="long-${steps}"
    d2_run run -kind long -steps "${steps}" -workflow "${wf}"
    d2_run dump -workflow "${wf}" | sed 's/^/  /'
    d2_run replay -workflow "${wf}" | grep '^ЗАМЕР'
done
