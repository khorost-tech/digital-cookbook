#!/usr/bin/env bash
# Task 10, сценарий 3 бенчмарка: "etcd под несвойственной нагрузкой".
#
# Поднимает Redis (compose/redis.yml, тот же контейнер/порт 6381, что и во
# всех остальных сценариях серии — эталон "правильного инструмента" для
# этого профиля) и ОТДЕЛЬНЫЙ, специфичный для этой задачи etcd
# (compose/etcd-misuse.yml, container inmemory-etcd-misuse, порт 2382,
# --quota-backend-bytes=67108864 — 64 МиБ вместо дефолтных 2 ГиБ, НЕ тот же
# контейнер/конфиг, что у Task 7: там квота не трогалась, watch/lease/
# lock/revisions её не касаются). Сеть inmemory-net в этом сценарии
# создаёт САМ compose/etcd-misuse.yml (см. комментарий в файле) — origin.yml
# здесь не поднимается вовсе: сценарий синтетический, PostgreSQL/dataset не
# нужны (см. заголовок scenarioEtcdMisuse в benchmark/main.go).
#
# Гоняет benchmark -scenario etcd-misuse, печатает результат на stdout
# (вызывающий заворачивает в tee scratchout/... сам, как во всех остальных
# задачах серии), гасит обе системы в конце (down -v).
set -euo pipefail
cd "$(dirname "$0")/.."

export GOPROXY="${GOPROXY:-https://go.khorost.tech,direct}"

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
# поздно"): та же ошибка, живьём пойманная ревьюером в
# compute-locality-demo.sh (падение на занятом порту оставило контейнер/сеть
# висеть, потому что trap стоял позже поднятия систем). `|| true` терпит
# "нечего гасить", если etcd/Redis не успели подняться до падения скрипта.
cleanup() {
    echo
    echo "== etcd-misuse-demo: гашу системы (down -v)"
    docker compose -f compose/redis.yml down -v 2>/dev/null || true
    docker compose -f compose/etcd-misuse.yml down -v 2>/dev/null || true
}
trap cleanup EXIT

echo "== etcd-misuse-demo: поднимаю etcd (квота 64 МиБ, создаёт inmemory-net) и Redis"
docker compose -f compose/etcd-misuse.yml up -d
docker compose -f compose/redis.yml up -d

echo "== жду готовности"
wait_for "etcd (misuse, квота 64 МиБ)" docker exec inmemory-etcd-misuse etcdctl endpoint health
wait_for "Redis" docker exec inmemory-redis redis-cli ping

mkdir -p scratchout

echo
echo "== etcd-misuse-demo: запускаю benchmark -scenario etcd-misuse"
(
    cd benchmark
    go mod tidy
    REDIS_ADDR=127.0.0.1:6381 \
        ETCD_MISUSE_ADDR=127.0.0.1:2382 \
        go run . -scenario etcd-misuse
)

echo
echo "== etcd-misuse-demo: готово"
