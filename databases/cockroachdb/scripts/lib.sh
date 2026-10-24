#!/usr/bin/env bash
#
# lib.sh — общие функции стенда CockroachDB. Подключается через source
# из каждого скрипта сценария. Сама по себе не выполняется.

COMPOSE_FILE="${COMPOSE_FILE:-compose/compose.yml}"
DB_NAME="${DB_NAME:-bank}"
PRIMARY_NODE="${PRIMARY_NODE:-crdb1}"

log()  { echo "[$(basename "${0%.sh}")] $*" >&2; }
fail() { echo "[$(basename "${0%.sh}")] ОШИБКА: $*" >&2; exit 1; }

# sql_at <узел> <аргументы cockroach sql> — выполняет SQL на указанном узле.
sql_at() {
    local node="$1"; shift
    docker exec -i "$node" cockroach sql --insecure --host=localhost:26257 \
        --database="$DB_NAME" "$@"
}

# То же самое на основном узле.
psql_c() { sql_at "$PRIMARY_NODE" "$@"; }

# Одно значение без заголовков и рамок.
sql_value() { psql_c --format=tsv -e "$1" | tail -n 1; }

# wait_healthy <имя контейнера> [попыток по 2с]
wait_healthy() {
    local name="$1" tries="${2:-60}" st
    for _ in $(seq 1 "$tries"); do
        st="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
              "$name" 2>/dev/null || echo missing)"
        [ "$st" = healthy ] && { log "$name: healthy"; return 0; }
        [ "$st" = missing ] && fail "контейнер $name не создан — docker compose up не прошёл"
        sleep 2
    done
    fail "контейнер $name не стал healthy за отведённое время"
}

# metric <узел> <http-порт хоста> <имя метрики>
#
# Возвращает СУММУ всех рядов метрики: часть метрик CockroachDB размечена
# по хранилищам, и брать первую строку означало бы молча терять остальные.
metric() {
    local node="$1" port="$2" name="$3" out
    out="$(curl -sf --max-time 5 "http://127.0.0.1:${port}/_status/vars")" \
        || fail "не удалось снять метрики с $node (порт $port)"
    printf '%s\n' "$out" | awk -v m="$name" '
        $1 == m || index($1, m "{") == 1 { s += $2; found = 1 }
        END { if (!found) exit 1; print s }
    ' || fail "метрика $name не найдена на $node"
}

# node_id_of <имя контейнера> — идентификатор узла.
#
# ЛОВУШКА: node_id НЕ выводится из имени контейнера. Идентификаторы
# раздаются в порядке присоединения к кластеру, поэтому crdb3 запросто
# оказывается узлом 2. Проверено на этом стенде: crdb3 -> node_id 2,
# crdb2 -> node_id 3. Всегда спрашиваем кластер.
node_id_of() {
    local name="$1" id
    id="$(docker exec "$PRIMARY_NODE" cockroach node status --insecure \
            --host=localhost:26257 --format=tsv 2>/dev/null \
          | awk -F'\t' -v h="${name}:26257" 'NR > 1 && $2 == h { print $1 }')"
    [ -n "$id" ] || fail "не удалось определить node_id для контейнера $name"
    printf '%s' "$id"
}

# Таблица соответствия контейнер -> node_id, для вывода в артефактах.
node_map() {
    docker exec "$PRIMARY_NODE" cockroach node status --insecure \
        --host=localhost:26257 --format=tsv 2>/dev/null \
      | awk -F'\t' 'NR == 1 { print "container\tnode_id"; next } { print $2 "\t" $1 }'
}

# Число живых узлов по мнению самого кластера.
nodes_live() {
    docker exec "$PRIMARY_NODE" cockroach node status --insecure \
        --host=localhost:26257 --format=tsv 2>/dev/null \
        | awk 'NR > 1 && $NF == "true" { n++ } END { print n + 0 }'
}

# require_cluster <сколько узлов> [секунд ожидания]
#
# Профили запускаются друг за другом, и предыдущий мог только что вернуть
# базовый кластер командой «compose start» — контейнеры при этом ещё не
# готовы. Мгновенная проверка nodes_live роняла следующий профиль на
# ровном месте, поэтому ждём, а не падаем сразу.
require_cluster() {
    local want="${1:-3}" secs="${2:-120}" i live
    for i in $(seq 1 $(( secs / 5 ))); do
        live="$(nodes_live 2>/dev/null || echo 0)"
        [ "$live" = "$want" ] && { log "живых узлов: $live"; return 0; }
        sleep 5
    done
    fail "за ${secs}с кластер не собрался: живых узлов ${live:-0}, нужно $want"
}
