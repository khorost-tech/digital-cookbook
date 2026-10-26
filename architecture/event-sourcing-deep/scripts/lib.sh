#!/usr/bin/env bash
# Общие функции стенда. Подключается через `source scripts/lib.sh`.

COMPOSE="compose/compose.yml"
PG_CONTAINER="esd-postgres"

# Порты хоста — переопределяемы через окружение, чтобы смена левой части
# `ports:` в compose/compose.yml (см. README, «Порты и как их поменять») не
# требовала правки скриптов: значения по умолчанию совпадают с
# compose/compose.yml на 2026-08-13. PG_DSN/ESDB_URL/KAFKA_BROKER тоже можно
# переопределить целиком (например, для подключения к стенду на другом
# хосте) — переменная порта тогда игнорируется.
PG_PORT="${PG_PORT:-15453}"
ESDB_PORT="${ESDB_PORT:-12113}"
KAFKA_PORT="${KAFKA_PORT:-49092}"
PG_DSN="${PG_DSN:-postgresql://esdemo:esdemo@127.0.0.1:${PG_PORT}/esdemo?sslmode=disable}"
ESDB_URL="${ESDB_URL:-esdb://127.0.0.1:${ESDB_PORT}?tls=false}"
KAFKA_BROKER="${KAFKA_BROKER:-127.0.0.1:${KAFKA_PORT}}"
export PG_DSN ESDB_URL KAFKA_BROKER PG_PORT ESDB_PORT KAFKA_PORT

log()  { echo "[$(basename "${0%.sh}")] $*"; }
fail() { echo "[$(basename "${0%.sh}")] ОШИБКА: $*" >&2; exit 1; }

# psql внутри контейнера: не требует psql на хосте и не зависит от локали.
psql_c() {
    docker exec -i "$PG_CONTAINER" psql -U esdemo -d esdemo -v ON_ERROR_STOP=1 "$@"
}

# Одно значение из одной строки. tail -n 1 отрезает заголовок.
# sed обрезает только края: tr -d ' ' вырезал бы ВСЕ пробелы и молча портил
# значения с пробелами внутри (version(), временные метки).
sql_value() {
    psql_c --tuples-only --no-align -c "$1" | tail -n 1 | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

wait_healthy() { # $1 = контейнер, $2 = секунд
    local c="$1" secs="${2:-60}" i state
    for i in $(seq 1 "$secs"); do
        state="$(docker inspect -f '{{.State.Health.Status}}' "$c" 2>/dev/null || echo none)"
        [ "$state" = "healthy" ] && { log "$c: healthy"; return 0; }
        sleep 1
    done
    fail "$c не стал healthy за ${secs}с (последнее состояние: ${state:-нет})"
}
