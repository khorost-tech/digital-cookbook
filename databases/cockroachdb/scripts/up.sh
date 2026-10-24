#!/usr/bin/env bash
#
# up.sh — поднять кластер из трёх узлов, инициализировать его и накатить схему.
# Идемпотентен: повторный запуск на живом стенде ничего не ломает.
#
# Требования: Docker (+ compose) в WSL2. Порты хоста: 15436-15438 (SQL),
# 18080-18082 (DB Console).
#
# Запуск (из databases/cockroachdb/):  bash scripts/up.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

# --- Preflight --------------------------------------------------------------

command -v docker >/dev/null 2>&1 || fail "docker не найден. Стенд поднимается из WSL2."
docker info >/dev/null 2>&1 || fail "docker-демон не отвечает."

for port in 15436 15437 15438; do
    occupant="$(docker ps --filter "publish=${port}" --format '{{.Names}}' 2>/dev/null || true)"
    case "$occupant" in
        ""|crdb1|crdb2|crdb3) ;;
        *) fail "порт ${port} занят чужим контейнером '${occupant}'" ;;
    esac
done

# --- Контейнеры -------------------------------------------------------------

log "Поднимаем три узла…"
docker compose -f "$COMPOSE_FILE" up -d crdb1 crdb2 crdb3

# --- Инициализация ----------------------------------------------------------
#
# cockroach init нужен ровно один раз на кластер. До того как узлы начнут
# слушать порт, init падает с отказом соединения — поэтому цикл. Ответ
# "already been initialized" означает успех: кластер уже собран.

log "Инициализация кластера…"
initialized=0
for attempt in $(seq 1 30); do
    out="$(docker exec crdb1 cockroach init --insecure --host=localhost:26257 2>&1 || true)"
    if printf '%s' "$out" | grep -q 'Cluster successfully initialized'; then
        log "кластер инициализирован (попытка $attempt)"
        initialized=1
        break
    fi
    if printf '%s' "$out" | grep -q 'already been initialized'; then
        log "кластер уже был инициализирован"
        initialized=1
        break
    fi
    sleep 2
done

if [ "$initialized" -ne 1 ]; then
    echo "$out" >&2
    fail "cockroach init не подтвердил инициализацию за 30 попыток"
fi

wait_healthy crdb1 60
wait_healthy crdb2 60
wait_healthy crdb3 60

live="$(nodes_live)"
[ "$live" -eq 3 ] || fail "ожидали 3 живых узла, кластер видит: $live"
log "Живых узлов: $live"

# --- Схема ------------------------------------------------------------------

log "Накатываем схему…"
docker exec -i crdb1 cockroach sql --insecure --host=localhost:26257 < sql/01-schema.sql >/dev/null

tables="$(sql_value "SELECT count(*) FROM [SHOW TABLES] WHERE table_name IN ('accounts_seq','accounts_uuid','hot_counter');")"
[ "$tables" -eq 3 ] || fail "ожидали 3 таблицы после схемы, нашли: $tables"
log "Таблиц в схеме: $tables"

log "Готово. SQL: postgresql://root@127.0.0.1:15436/bank?sslmode=disable"
log "DB Console: http://127.0.0.1:18080"
log "Дальше: bash scripts/probe.sh — зафиксировать фактические версии и настройки."
