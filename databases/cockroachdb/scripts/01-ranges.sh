#!/usr/bin/env bash
#
# 01-ranges.sh — артефакт к статье «Архитектура: ranges, Raft и leaseholder».
#
# Три замера:
#   A. как диапазоны и реплики разложены по трём узлам;
#   B. как растёт число диапазонов, когда таблица переваливает за
#      range_max_bytes (split);
#   C. что происходит с арендами, когда узел гаснет.
#
# Скрипт ВОССТАНАВЛИВАЕТ погашенный узел на выходе, в том числе при падении.
#
# ВАЖНО про источники данных: начиная с 26.x доступ к crdb_internal закрыт
# («Access to crdb_internal and system is restricted»), поэтому всё берётся
# из поддерживаемых SHOW CLUSTER RANGES / SHOW RANGES ... WITH DETAILS.
#
# Запуск (из databases/cockroachdb/):  bash scripts/01-ranges.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

DOWN_NODE=""
restore() {
    if [ -n "$DOWN_NODE" ]; then
        log "восстанавливаем $DOWN_NODE…"
        docker start "$DOWN_NODE" >/dev/null 2>&1 || true
        wait_healthy "$DOWN_NODE" 60 || true
    fi
}
trap restore EXIT

[ "$(nodes_live)" -eq 3 ] || fail "перед прогоном нужен кластер из 3 живых узлов"

echo "=== A. Распределение реплик и аренд по узлам ==="
# Соответствие печатаем первым делом: без него весь дальнейший вывод
# читается неверно — node_id не совпадает с номером в имени контейнера.
echo "--- контейнер -> node_id:"
node_map
psql_c -e "SELECT node_id, count(*) AS replicas
           FROM (SELECT unnest(replicas) AS node_id FROM [SHOW CLUSTER RANGES])
           GROUP BY node_id ORDER BY node_id;"
psql_c -e "SELECT lease_holder, count(*) AS leases
           FROM [SHOW CLUSTER RANGES WITH DETAILS]
           GROUP BY lease_holder ORDER BY lease_holder;"

echo
echo "=== B. Split под ростом таблицы ==="
# Отдельная таблица с широким payload: строки accounts_* слишком узкие,
# чтобы за разумное время перевалить за минимально допустимый размер
# диапазона. Минимум подтверждён probe.sh: range_max_bytes не может быть
# меньше 64 МиБ, поэтому наливаем заведомо больше.
batch=50000
batches=14
target_rows=$((batch * batches))

psql_c -e "CREATE TABLE IF NOT EXISTS bulk_rows (id INT8 PRIMARY KEY, payload STRING NOT NULL);" >/dev/null
psql_c -e "ALTER TABLE bulk_rows CONFIGURE ZONE USING range_min_bytes = 33554432, range_max_bytes = 67108864;" >/dev/null

have="$(sql_value "SELECT count(*) FROM bulk_rows;")"
if [ "$have" -ge "$target_rows" ]; then
    log "таблица уже налита ($have строк) — налив пропускаем"
    before=""
    after="$(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE bulk_rows];")"
    rows="$have"
    log "диапазонов сейчас: $after"
else
    before="$(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE bulk_rows];")"
    log "до налива: диапазонов=$before"

    # Серверный INSERT ... SELECT батчами: одна транзакция на 700k строк
    # упёрлась бы в лимит на размер транзакции.
    for i in $(seq 0 $((batches - 1))); do
        from=$((i * batch + 1))
        to=$(((i + 1) * batch))
        psql_c -e "INSERT INTO bulk_rows (id, payload)
                   SELECT g, repeat('x', 256) FROM generate_series($from, $to) AS g
                   ON CONFLICT (id) DO NOTHING;" >/dev/null
        printf '\r  налито строк: %d' "$to" >&2
    done
    printf '\n' >&2

    rows="$(sql_value "SELECT count(*) FROM bulk_rows;")"
    log "строк в таблице: $rows"

    # Split происходит асинхронно — даём кластеру устояться, а не
    # констатируем сразу после последней вставки.
    log "ждём 60с, пока кластер разделит диапазоны…"
    sleep 60

    after="$(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE bulk_rows];")"
    log "после налива: диапазонов=$after"
fi

psql_c -e "SELECT range_id, range_size_mb::DECIMAL(10,1) AS size_mb, lease_holder, replicas
           FROM [SHOW RANGES FROM TABLE bulk_rows WITH DETAILS]
           ORDER BY range_id;"

[ "$rows" -ge "$target_rows" ] || fail "налито меньше строк, чем задумано: $rows"
if [ -z "$before" ]; then
    log "сравнение «до/после» в этом прогоне не делается — таблица была налита заранее"
elif [ "$after" -le "$before" ]; then
    log "ВНИМАНИЕ: число диапазонов не выросло ($before → $after)."
    log "Это НЕ значит «split не работает» — значит, объёма не хватило."
    log "В FIXTURES писать факт и объём, либо повторить с бо́льшим наливом."
else
    log "диапазонов стало больше: $before → $after"
fi

echo
echo "=== C. Гашение узла-арендатора и перерыв в обслуживании ==="
#
# Меряем две разные вещи, которые легко перепутать:
#   C1 — сколько времени конкретный ключ недоступен для чтения;
#   C2 — за сколько аренды целиком покидают мёртвый узел.
#
# Аренду целевого диапазона переносим на тот узел, который собираемся
# гасить: иначе погасили бы узел, к нашим данным отношения не имеющий,
# и получили бы красивое число ни о чём.

KILL_CONTAINER=crdb3
kill_id="$(node_id_of "$KILL_CONTAINER")"
log "гасить будем контейнер $KILL_CONTAINER — это node_id $kill_id"

target_range="$(sql_value "SELECT range_id FROM [SHOW RANGES FROM TABLE bulk_rows] ORDER BY range_id LIMIT 1;")"
log "целевой диапазон (содержит id=1): $target_range"

psql_c -e "ALTER RANGE $target_range RELOCATE LEASE TO $kill_id;" >/dev/null
sleep 3
holder="$(sql_value "SELECT lease_holder FROM [SHOW RANGES FROM TABLE bulk_rows WITH DETAILS] WHERE range_id = $target_range;")"
[ "$holder" = "$kill_id" ] || fail "аренда диапазона $target_range не переехала на узел $kill_id (сейчас: $holder) — гасить нечего"
log "аренда диапазона $target_range на узле $kill_id (контейнер $KILL_CONTAINER)"

STOPF=/tmp/crdb-01-stop
LOGF=/tmp/crdb-01-probe.log
rm -f "$STOPF" "$LOGF"

# Пробник читает ОДИН ключ из целевого диапазона. timeout ограничивает
# каждую пробу: без него зависшая проба съела бы весь замер и выдала
# ложную непрерывность.
probe_loop() {
    while [ ! -f "$STOPF" ]; do
        if timeout 10 docker exec -i crdb1 cockroach sql --insecure \
             --host=localhost:26257 --database=bank \
             -e "SELECT payload FROM bulk_rows WHERE id = 1;" >/dev/null 2>&1; then
            echo "$(date +%s.%N) ok" >> "$LOGF"
        else
            echo "$(date +%s.%N) fail" >> "$LOGF"
        fi
    done
}

probe_loop &
PROBE_PID=$!

log "базовая линия 10с…"
sleep 10

DOWN_NODE="$KILL_CONTAINER"
log "гасим $KILL_CONTAINER (node_id $kill_id)…"
docker stop "$KILL_CONTAINER" >/dev/null

# C2: попытка измерить «за сколько аренды покидают мёртвый узел» — и
# ОТРИЦАТЕЛЬНЫЙ результат, который важнее самого замера.
#
# Счётчик аренд на погашенном узле обнуляется МГНОВЕННО (0 с на первом же
# опросе). Это не скорость переезда: недоступный узел просто перестаёт
# фигурировать в SHOW CLUSTER RANGES, и ноль означает отсутствие данных,
# а не завершённую миграцию. Инструмент на этот вопрос не отвечает —
# фиксируем это как факт и не выдаём ноль за результат.
killed_at="$(date +%s)"
n="$(sql_value "SELECT count(*) FROM [SHOW CLUSTER RANGES WITH DETAILS] WHERE lease_holder = $kill_id;" 2>/dev/null || echo "?")"
log "аренд на узле $kill_id сразу после гашения: $n (за $(( $(date +%s) - killed_at ))с)"
log "ноль здесь = представление больше не видит узел, а НЕ «миграция завершилась»"

log "наблюдаем чтение ещё 60с…"
sleep 60

touch "$STOPF"
wait "$PROBE_PID" || true

samples="$(wc -l < "$LOGF")"
oks="$(grep -c ' ok$' "$LOGF" || true)"
fails="$(grep -c ' fail$' "$LOGF" || true)"
log "проб всего: $samples (успешных: $oks, неуспешных: $fails)"
[ "$samples" -ge 30 ] || fail "проб слишком мало ($samples) — замер несостоятелен"
[ "$oks" -ge 10 ] || fail "успешных проб слишком мало ($oks) — читать было нечего"

# Максимальный разрыв между успешными пробами и есть перерыв в
# обслуживании: он ловит и явные отказы, и случай, когда клиент просто
# долго ждёт новой аренды, не получая ошибки.
awk '$2 == "ok" { if (prev != "") { d = $1 - prev; if (d > max) max = d } prev = $1 }
     END { printf "  максимальный разрыв между успешными чтениями: %.1f с\n", max }' "$LOGF"
awk '$2 == "fail" { c++ } END { printf "  проб с явной ошибкой: %d\n", c + 0 }' "$LOGF"

log "аренда целевого диапазона после гашения:"
psql_c -e "SELECT range_id, lease_holder FROM [SHOW RANGES FROM TABLE bulk_rows WITH DETAILS]
           WHERE range_id = $target_range;"

log "распределение аренд при погашенном узле (мёртвый узел в выводе отсутствует):"
psql_c -e "SELECT lease_holder, count(*) AS leases
           FROM [SHOW CLUSTER RANGES WITH DETAILS]
           GROUP BY lease_holder ORDER BY lease_holder;"

restore
DOWN_NODE=""
live="$(nodes_live)"
[ "$live" -eq 3 ] || fail "узел не вернулся в строй, живых: $live"
log "узел вернулся, живых: $live"
