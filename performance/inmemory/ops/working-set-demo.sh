#!/usr/bin/env bash
# Task 8, сценарий 1: "рабочий набор за границей RAM".
#
# Поднимает origin (PostgreSQL, источник истины датасета), Redis
# (compose/redis.yml, тот же контейнер, что и Task 2 — maxmemory/policy
# конфигурируются здесь ДИНАМИЧЕСКИ через CONFIG SET, сам compose-файл не
# трогается) и ОТДЕЛЬНЫЙ, специфичный для этой задачи Aerospike
# (compose/aerospike-workingset.yml, container inmemory-aerospike-ws,
# порт 3010 — НЕ тот же контейнер/конфиг, что у Task 5: там лимиты
# намеренно большие, здесь — намеренно маленькие, см.
# aerospike/aerospike-workingset.conf). Гоняет benchmark -scenario
# working-set, печатает результат на stdout (вызывающий заворачивает в
# tee scratchout/... сам, как во всех остальных задачах серии), гасит все
# три системы в конце (down -v).
set -euo pipefail
cd "$(dirname "$0")/.."

export GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}"

ORIGIN_DSN_LOCAL='postgres://inmemory:inmemory@127.0.0.1:5433/catalog?sslmode=disable'
REDIS_MAXMEMORY=8000000

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

# trap регистрируется ЗДЕСЬ, ДО первого `docker compose up` ниже — ВТОРОЙ
# РАУНД ВНЕШНЕГО РЕВЬЮ (18.07, замечание 2 — "cleanup регистрируется слишком
# поздно"): в compute-locality-demo.sh живой прогон ревьюера упал на занятом
# порту 5433 раньше, чем скрипт доходил до `trap`, стоявшего в конце — и
# оставил контейнер/сеть висеть. Тот же класс ошибки был и здесь (cleanup
# определялся только перед запуском benchmark, а не перед поднятием систем
# выше). `|| true`/`-v` терпят "нечего гасить", если что-то из систем не
# успело подняться до падения скрипта.
cleanup() {
    echo
    echo "== working-set-demo: гашу системы (down -v)"
    docker compose -f compose/aerospike-workingset.yml down -v 2>/dev/null || true
    docker compose -f compose/redis.yml down -v 2>/dev/null || true
    docker compose -f compose/origin.yml down -v 2>/dev/null || true
}
trap cleanup EXIT

echo "== working-set-demo: поднимаю системы с фиксированными лимитами"
docker compose -f compose/origin.yml up -d
docker compose -f compose/redis.yml up -d
docker compose -f compose/aerospike-workingset.yml up -d

echo "== жду готовности"
wait_for "PostgreSQL (origin)" docker exec inmemory-origin pg_isready -U inmemory -d catalog
wait_for "Redis" docker exec inmemory-redis redis-cli ping
wait_for "Aerospike (working-set)" docker exec inmemory-aerospike-ws asinfo -v status

echo "== датасет"
COUNT=$(docker exec inmemory-origin psql -U inmemory -d catalog -tAc "select count(*) from products" 2>/dev/null | tr -d '[:space:]' || echo 0)
if [ "$COUNT" != "200000" ]; then
    echo "  products=$COUNT (ожидается 200000) — загружаю датасет"
    (cd dataset && go mod tidy && ORIGIN_DSN="$ORIGIN_DSN_LOCAL" go run . -load)
else
    echo "  датасет уже загружен (products=200000)"
fi

echo "== фиксирую лимит Redis: maxmemory=$REDIS_MAXMEMORY, maxmemory-policy=allkeys-lru"
docker exec inmemory-redis redis-cli CONFIG SET maxmemory "$REDIS_MAXMEMORY" >/dev/null
docker exec inmemory-redis redis-cli CONFIG SET maxmemory-policy allkeys-lru >/dev/null

echo "== лимиты Aerospike (зафиксированы в aerospike/aerospike-workingset.conf, перечитываю живьём)"
docker exec inmemory-aerospike-ws asinfo -v "get-config:context=namespace;id=ram" | tr ';' '\n' | grep -E "^storage-engine\.(data-size|stop-writes-used-pct)="
docker exec inmemory-aerospike-ws asinfo -v "get-config:context=namespace;id=flash" | tr ';' '\n' | grep -E "^indexes-memory-budget="

mkdir -p scratchout

echo
echo "== working-set-demo: запускаю benchmark -scenario working-set"
(
    cd benchmark
    go mod tidy
    ORIGIN_DSN="$ORIGIN_DSN_LOCAL" \
        REDIS_ADDR=127.0.0.1:6381 \
        AEROSPIKE_ADDR=127.0.0.1:3010 \
        go run . -scenario working-set
)

echo
echo "== working-set-demo: готово"
