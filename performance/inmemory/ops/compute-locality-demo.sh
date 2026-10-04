#!/usr/bin/env bash
# Task 9, сценарий 2 бенчмарка: "цена выноса вычислений к данным".
#
# Поднимает origin (PostgreSQL, источник истины датасета), Tarantool
# (compose/tarantool.yml, контейнер inmemory-tarantool, порт 3301 — тот же
# контракт top_products_by_category, что и Стенд 3/Task 3) и Ignite
# (compose/ignite.yml, ignite-1/ignite-2 — тот же TopByCategoryTask, что и
# Стенд 6/Task 6). Пересобирает ignite-stand.jar ПЕРЕД поднятием Ignite: jar
# смонтирован в контейнеры как read-only том (см. compose/ignite.yml), а
# Task 9 добавил в него -order (см. benchmark/main.go, заголовок раздела
# compute-locality) — устаревший jar без -order сценарий compute-locality
# не запустит.
#
# Гоняет benchmark -scenario compute-locality ДВАЖДЫ — обычным порядком и с
# -swap-order (Step 2 брифа, "Защита от смещения порядка"): каждый отдельный
# `go run` — это ОТДЕЛЬНЫЙ TCP-коннект к Tarantool и ОТДЕЛЬНЫЙ `docker run`
# (свежая JVM) для Ignite, так что сравнение прямого/обратного запуска здесь
# защищает не только от прогрева кэша ВНУТРИ одного соединения (это
# сценарий уже делает сам, см. main.go), но и от прогрева между отдельными
# процессами/JVM.
#
# Гасит все системы в конце (down -v/down) — trap, а не безусловный вызов
# после команды: при set -e любой ненулевой выход benchmark (например,
# сработавший АССЕРТ) обрывает скрипт немедленно, без trap контейнеры
# остались бы висеть.
set -euo pipefail
cd "$(dirname "$0")/.."

export GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}"
# Git Bash (MSYS) на Windows переписывает аргументы вида "/app" в
# "C:/Program Files/Git/app" при разборе командной строки docker run — живой
# прогон подтвердил (docker: "the working directory 'C:/Program Files/Git/app'
# is invalid"). MSYS_NO_PATHCONV отключает это переписывание для всего скрипта.
export MSYS_NO_PATHCONV=1

ORIGIN_DSN_LOCAL='postgres://inmemory:inmemory@127.0.0.1:5433/catalog?sslmode=disable'
ORIGIN_DSN_INNET='postgres://inmemory:inmemory@inmemory-origin:5432/catalog?sslmode=disable'

wait_for() {
    local desc="$1"; shift
    local attempts=60
    for ((i = 0; i < attempts; i++)); do
        if "$@" >/dev/null 2>&1; then
            echo "  $desc: готов"
            return 0
        fi
        sleep 1
    done
    echo "БЛОК: $desc не поднялся за ${attempts}с" >&2
    exit 1
}

echo "== compute-locality-demo: собираю ignite-stand.jar (нужен -order, добавленный Task 9 поверх Стенда 6)"
(
    cd ignite
    docker run --rm -v "$(pwd):/app" -v "$(pwd)/../.m2cache:/root/.m2" -w /app \
        maven:3.9-eclipse-temurin-21 mvn -q clean package
)

cleanup() {
    echo
    echo "== compute-locality-demo: гашу системы"
    docker compose -f compose/ignite.yml down 2>/dev/null || true
    docker compose -f compose/tarantool.yml down -v 2>/dev/null || true
    docker compose -f compose/origin.yml down -v 2>/dev/null || true
}
# ВТОРОЙ РАУНД ВНЕШНЕГО РЕВЬЮ (18.07, замечание 2 — "cleanup регистрируется
# слишком поздно"): trap регистрируется ЗДЕСЬ, ДО первого `docker compose up`
# ниже, а не после него. Живой прогон ревьюера упал на занятом порту 5433
# ДО того места, где раньше стоял `trap cleanup EXIT`, — скрипт прервался по
# `set -e` и оставил контейнер/сеть висеть, убирать пришлось руками. Функция
# cleanup переживает падение на любом более раннем этапе (частично поднятые
# или вовсе не поднятые сервисы) — `|| true`/`2>/dev/null` гасят ошибку
# "нечего гасить" у `docker compose down`, не прерывая сам cleanup.
trap cleanup EXIT

echo "== compute-locality-demo: поднимаю origin, Tarantool, Ignite (2 узла)"
docker compose -f compose/origin.yml up -d
docker compose -f compose/tarantool.yml up -d
docker compose -f compose/ignite.yml up -d

echo "== жду готовности"
wait_for "PostgreSQL (origin)" docker exec inmemory-origin pg_isready -U inmemory -d catalog
wait_for "Tarantool" bash -c "[ \"\$(docker inspect -f '{{.State.Health.Status}}' inmemory-tarantool 2>/dev/null)\" = healthy ]"

# Ignite не публикует healthcheck в compose/ignite.yml (см. Стенд 6) —
# README «Как воспроизвести» ждёт кольцо discovery фиксированным sleep 25с,
# здесь тот же бюджет, но с явной проверкой результата вместо слепого
# продолжения: без "servers=2" в логах кластер не собрался, и запуск
# compute-locality на нём дал бы недоказательный (не collocated) результат.
echo "  Ignite: жду кольцо discovery (~25с)"
sleep 25
IGNITE_SERVERS=$(docker logs ignite-1 2>&1 | grep -o "servers=[0-9]*" | tail -1 || true)
echo "  Ignite: $IGNITE_SERVERS"
if [ "$IGNITE_SERVERS" != "servers=2" ]; then
    echo "БЛОК: Ignite discovery не собрал кольцо из 2 узлов ($IGNITE_SERVERS) — compute-locality на нём недоказателен" >&2
    exit 1
fi

echo "== датасет"
COUNT=$(docker exec inmemory-origin psql -U inmemory -d catalog -tAc "select count(*) from products" 2>/dev/null | tr -d '[:space:]' || echo 0)
if [ "$COUNT" != "200000" ]; then
    echo "  products=$COUNT (ожидается 200000) — загружаю датасет"
    (cd dataset && go mod tidy && ORIGIN_DSN="$ORIGIN_DSN_LOCAL" go run . -load)
else
    echo "  датасет уже загружен (products=200000)"
fi

mkdir -p scratchout

echo
echo "== compute-locality-demo: benchmark -scenario compute-locality (порядок ПРЯМОЙ)"
(
    cd benchmark
    go mod tidy
    ORIGIN_DSN="$ORIGIN_DSN_LOCAL" \
        ORIGIN_DSN_INNET="$ORIGIN_DSN_INNET" \
        TARANTOOL_ADDR=127.0.0.1:3301 \
        go run . -scenario compute-locality
)

echo
echo "== compute-locality-demo: benchmark -scenario compute-locality (порядок ОБРАТНЫЙ, -swap-order)"
(
    cd benchmark
    ORIGIN_DSN="$ORIGIN_DSN_LOCAL" \
        ORIGIN_DSN_INNET="$ORIGIN_DSN_INNET" \
        TARANTOOL_ADDR=127.0.0.1:3301 \
        go run . -scenario compute-locality -swap-order
)

echo
echo "== compute-locality-demo: готово"
