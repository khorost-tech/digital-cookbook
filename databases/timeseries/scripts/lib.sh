#!/usr/bin/env bash
#
# lib.sh — общие функции стенда timeseries. Подключается через source.

COMPOSE="docker compose -f compose/compose.yml"
GEN="docker run --rm --network tsdb tsdb-gen"
FIXTURES_DIR="${FIXTURES_DIR:-fixtures}"

log()  { echo "[$(basename "${0%.sh}")] $*" >&2; }
fail() { echo "[$(basename "${0%.sh}")] ОШИБКА: $*" >&2; exit 1; }

# sql <SQL> — одно значение без заголовков, для проверок внутри скриптов.
sql() { docker exec tsdb-ts psql -U postgres -d ts -v ON_ERROR_STOP=1 -tAc "$1"; }

# fresh <сервис…> — пересоздать сервисы с пустым хранилищем (тома не именованы,
# данные живут в слое контейнера и уходят вместе с ним).
fresh() {
    $COMPOSE rm -sf "$@" >/dev/null 2>&1
    $COMPOSE up -d --wait "$@" >/dev/null 2>&1 || $COMPOSE up -d --wait "$@" >/dev/null 2>&1 || fail "не поднялись: $*"
}

# wait_http <контейнер> <url> — ждать ответа 200 изнутри контейнера.
wait_http() {
    local i
    for i in $(seq 1 60); do
        docker exec "$1" wget -qO- "$2" >/dev/null 2>&1 && return 0
        sleep 1
    done
    fail "$1: $2 не отвечает"
}

# capture <файл> <заголовок> <команда> — выполнить, показать, дописать секцию
# в fixtures/<файл>.txt. Код возврата команды — в CAPTURE_RC, вывод — в CAPTURE_OUT.
capture() {
    local file="$FIXTURES_DIR/$1.txt" title="$2" cmd="$3"
    mkdir -p "$FIXTURES_DIR"
    CAPTURE_OUT="$(bash -c "$cmd" 2>&1)"; CAPTURE_RC=$?
    {
        echo "--- $title ---"
        echo "# cmd: $cmd"
        printf '%s\n' "$CAPTURE_OUT"
        echo "# код возврата: $CAPTURE_RC"
        echo
    } >> "$file"
    printf '\n--- %s ---\n$ %s\n%s\n# код возврата: %s\n' "$title" "$cmd" "$CAPTURE_OUT" "$CAPTURE_RC" >&2
}

fixture_header() {
    mkdir -p "$FIXTURES_DIR"
    {
        echo "# $1"
        echo "# снято: $(date -u '+%Y-%m-%d %H:%M UTC'), хост: $(uname -sm), Docker $(docker version --format '{{.Server.Version}}')"
        echo
    } > "$FIXTURES_DIR/$1.txt"
}

# psql_cmd <SQL> — строка команды psql для capture (то, что увидит читатель).
psql_cmd() { printf 'docker exec tsdb-ts psql -U postgres -d ts -c "%s"' "$1"; }
