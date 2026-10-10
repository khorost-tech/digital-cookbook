#!/usr/bin/env bash
# Общие функции стенда. Подключается через `source scripts/lib.sh`.
#
# Главное правило стенда: НИЧЕГО не запускается с хоста напрямую. Локально
# собранные бинари на рабочей Windows-машине не достукиваются до
# localhost:7253 — dial висит на [::1]/IPv4 и отваливается по таймауту.
# Всё, что ходит в Temporal, запускается контейнером на сети стенда и
# обращается к temporal-frontend:7233.

set -euo pipefail

STAND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${STAND_DIR}/compose/compose.yml"
NET="temporal-cookbook_default"
TEMPORAL_ADDR="temporal-frontend:7233"
GO_IMAGE="golang:1.26.3-alpine"
ADMIN_IMAGE="temporalio/auto-setup:1.29.7"
# Отдельный том под кеш модулей и сборки: без него каждый запуск go
# скачивает зависимости заново, и профиль идёт втрое дольше.
GOCACHE_VOL="temporal-cookbook-gocache"

# Печать заголовка раздела — чтобы вывод профиля читался как отчёт.
section() {
    echo
    echo "=============================================================="
    echo "  $*"
    echo "=============================================================="
}

# Запуск произвольного образа на сети стенда.
# run_in_net <имя-контейнера> <образ> <аргументы...>
run_in_net() {
    local name="$1" image="$2"; shift 2
    docker run --rm --name "${name}" --network "${NET}" "${image}" "$@"
}

# Вызов Temporal CLI на сети стенда.
# У образа auto-setup собственный entrypoint, который поднимает сервер и
# игнорирует переданные аргументы, — поэтому entrypoint переопределяется.
# cli_in_net <имя-контейнера> <аргументы temporal...>
cli_in_net() {
    local name="$1"; shift
    docker run --rm --name "${name}" --network "${NET}" \
        --entrypoint temporal "${ADMIN_IMAGE}" "$@"
}

# Общие аргументы docker run для Go-контейнеров.
# Каталог go монтируется НА ЗАПИСЬ: go run с -mod=mod правит go.sum,
# а на read-only это падает не по существу задачи.
_go_docker_args() {
    printf '%s\n' \
        --network "${NET}" \
        -v "${STAND_DIR}/go:/src" \
        -v "${GOCACHE_VOL}:/gocache" \
        -w /src \
        -e CGO_ENABLED=0 \
        -e GOFLAGS=-mod=mod \
        -e GOMODCACHE=/gocache/mod \
        -e GOCACHE=/gocache/build \
        -e GOPROXY="https://go.khorost.tech,direct"
}

# Сборка профиля в образе golang и запуск на сети стенда.
# go_run_in_net <имя-контейнера> <профиль> <аргументы...>
#
# Собираем ВНУТРИ образа golang, а не на хосте: хостовая сборка под
# Windows даёт бинарь не того GOOS, а сборка на drvfs периодически
# роняет запись артефактов.
#
# ВАЖНО: подкоманда идёт ПЕРВЫМ аргументом программы, а -address —
# после неё. Точки входа профилей разбирают os.Args[1] как подкоманду и
# только потом парсят флаги; поставить -address впереди — значит скормить
# программе «неизвестную подкоманду -address».
go_run_in_net() {
    local name="$1" profile="$2" sub="$3"; shift 3
    local args=()
    mapfile -t args < <(_go_docker_args)
    docker run --rm --name "${name}" "${args[@]}" \
        "${GO_IMAGE}" go run "./${profile}" "${sub}" -address "${TEMPORAL_ADDR}" "$@"
}

# Тот же запуск, но в фоне: возвращает имя контейнера, который потом убивают.
go_start_in_net() {
    local name="$1" profile="$2" sub="$3"; shift 3
    local args=()
    mapfile -t args < <(_go_docker_args)
    docker run -d --name "${name}" "${args[@]}" \
        "${GO_IMAGE}" go run "./${profile}" "${sub}" -address "${TEMPORAL_ADDR}" "$@" >/dev/null
    echo "${name}"
}

# Одно значение одной строкой из Postgres.
# Обрезаем ТОЛЬКО края: `tr -d ' '` вырезал бы и внутренние пробелы, молча
# превращая «PostgreSQL 18.4 (Debian…)» в склеенную кашу, и подделка уехала
# бы в фикстуры незамеченной.
pg_query() {
    docker exec -i temporal-postgres psql -U temporal -d temporal -t -A -c "$1" \
        | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'
}

# GET к Elasticsearch изнутри его же контейнера (curl на хосте не нужен).
es_get() {
    docker exec -i temporal-elasticsearch curl -sf "http://localhost:9200$1"
}

# Мгновенное значение PromQL. Возвращает пустую строку, если ряда нет —
# вызывающий ОБЯЗАН это проверить: «нет ряда» и «ноль» — разные факты.
prom_query() {
    docker exec -i temporal-prometheus \
        wget -qO- "http://localhost:9090/api/v1/query?query=$(printf '%s' "$1" | sed 's/ /%20/g')" \
        | sed -n 's/.*"value":\[[^,]*,"\([^"]*\)"\].*/\1/p'
}

now_ms() { date +%s%3N; }

# Ожидание строки в логе контейнера. Падает по таймауту, а не висит вечно.
# wait_for_log <контейнер> <подстрока> <секунд>
wait_for_log() {
    local c="$1" needle="$2" limit="${3:-60}" waited=0
    while (( waited < limit )); do
        if docker logs "${c}" 2>&1 | grep -qF -- "${needle}"; then return 0; fi
        sleep 1; waited=$((waited + 1))
    done
    echo "ОШИБКА: в логе ${c} за ${limit}s не появилось: ${needle}" >&2
    docker logs --tail 40 "${c}" >&2 || true
    return 1
}
