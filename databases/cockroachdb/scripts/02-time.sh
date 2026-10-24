#!/usr/bin/env bash
#
# 02-time.sh — артефакт к статье «MVCC, HLC и uncertainty».
#
# Четыре замера:
#   A. отставание closed timestamp: насколько в прошлом живёт follower read;
#   B. latency чтения с leaseholder против follower read;
#   C. глубина AS OF SYSTEM TIME и её связь с gc.ttlseconds;
#   D. uncertainty restarts при расширенном --max-offset — и почему их ноль.
#
# Про D отдельно. Ноль здесь — не недоработка нагрузки, а следствие того,
# что все узлы стенда делят часы ядра хоста. Расхождение часов на одной
# машине невоспроизводимо: time namespace в Linux виртуализирует только
# монотонные часы, а libfaketime перехватывает вызовы libc и на Go-бинарь
# (каким CockroachDB и является) не действует — проверено опытом, см.
# FIXTURES.md. Поэтому D фиксирует именно отсутствие ретраев как факт.
#
# Кластер перезапускается с оверлеем time.yml и возвращается на базовый
# профиль в конце.
#
# Запуск (из databases/cockroachdb/):  bash scripts/02-time.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

restore_base() {
    log "возвращаем базовый профиль…"
    docker compose -f compose/compose.yml up -d --force-recreate >/dev/null 2>&1 || true
}
trap restore_base EXIT

log "перезапускаем кластер с --max-offset=2s…"
docker compose -f compose/compose.yml -f compose/time.yml up -d --force-recreate >/dev/null
wait_healthy crdb1 60; wait_healthy crdb2 60; wait_healthy crdb3 60
[ "$(nodes_live)" -eq 3 ] || fail "кластер не собрался с оверлеем time.yml"

# Подтверждаем, что оверлей ДЕЙСТВИТЕЛЬНО применился, а не был молча
# проигнорирован: без этой проверки весь замер D ничего не значит.
docker inspect crdb1 --format '{{json .Config.Cmd}}' | grep -q -- '--max-offset=2s' \
    || fail "--max-offset=2s не попал в аргументы crdb1 — оверлей не применился"
log "оверлей применён, окно неопределённости расширено до 2с"

echo "--- контейнер -> node_id:"
node_map

echo
echo "=== A. Отставание closed timestamp ==="
psql_c -e "SELECT variable, value FROM [SHOW ALL CLUSTER SETTINGS]
           WHERE variable IN ('kv.closed_timestamp.target_duration',
                              'kv.closed_timestamp.propagation_slack',
                              'kv.closed_timestamp.side_transport_interval');"
log "насколько follower read отстаёт от настоящего времени (5 замеров):"
for _ in 1 2 3 4 5; do
    psql_c --format=tsv -e "SELECT extract(epoch FROM now() - follower_read_timestamp())::DECIMAL(6,3) AS lag_sec;" | tail -1
    sleep 1
done

echo
echo "=== B. Leaseholder read против follower read ==="
#
# Сравнивать latency двух запросов НЕДОСТАТОЧНО: AS OF SYSTEM TIME
# follower_read_timestamp() задаёт лишь метку в прошлом, а обслужить
# такой запрос может и сам арендатор. Тогда мы сравнивали бы обычное
# чтение с обычным чтением на старой метке.
#
# Признак фактического обслуживания репликой — счётчик
# follower_reads_success_count («reads successfully processed by any
# replica»). Снимаем его вокруг каждого запроса: расти он обязан только
# на follower-режиме.
fr_total() {
    local sum=0 p v
    for p in 18080 18081 18082; do
        v="$(curl -sf --max-time 5 "http://127.0.0.1:${p}/_status/vars" \
             | awk '/^follower_reads_success_count/ { s += $2 } END { print s + 0 }')" \
            || fail "не удалось снять follower_reads_success_count с порта $p"
        sum=$(( sum + v ))
    done
    printf '%s' "$sum"
}

#
# ЗАВИСИМОСТЬ МЕЖДУ ПРОФИЛЯМИ, на которой этот скрипт уже падал: таблицу
# bulk_rows создаёт 01-ranges.sh, и если базовый кластер пересоздавали, её
# просто нет. Профиль обязан быть самодостаточным — создаём при отсутствии.
bulk_have="$(sql_value "SELECT count(*) FROM bulk_rows;" 2>/dev/null || echo 0)"
case "$bulk_have" in ''|*[!0-9]*) bulk_have=0 ;; esac
if [ "$bulk_have" -lt 700000 ]; then
    log "bulk_rows отсутствует или неполна ($bulk_have строк) — наливаем, это займёт минуты"
    psql_c -e "CREATE TABLE IF NOT EXISTS bulk_rows (id INT8 PRIMARY KEY, payload STRING NOT NULL);" >/dev/null
    psql_c -e "ALTER TABLE bulk_rows CONFIGURE ZONE USING range_min_bytes = 33554432, range_max_bytes = 67108864;" >/dev/null
    for i in $(seq 0 13); do
        from=$((i * 50000 + 1)); to=$(((i + 1) * 50000))
        psql_c -e "INSERT INTO bulk_rows (id, payload)
                   SELECT g, repeat('x', 256) FROM generate_series($from, $to) AS g
                   ON CONFLICT (id) DO NOTHING;" >/dev/null
        printf '  налито строк: %d' "$to" >&2
    done
    printf '
' >&2
    bulk_have="$(sql_value "SELECT count(*) FROM bulk_rows;")"
    [ "$bulk_have" -ge 700000 ] || fail "не удалось налить bulk_rows: $bulk_have строк"
fi
log "bulk_rows: $bulk_have строк"

log "где сейчас арендаторы целевой таблицы (от этого зависит, что с чем сравниваем):"
psql_c -e "SELECT range_id, lease_holder, lease_holder_locality
           FROM [SHOW RANGES FROM TABLE bulk_rows WITH DETAILS] ORDER BY range_id;"

psql_c -e "SELECT count(*) FROM bulk_rows;" >/dev/null
psql_c -e "SELECT count(*) FROM bulk_rows AS OF SYSTEM TIME follower_read_timestamp();" >/dev/null

# Режимы ЧЕРЕДУЮТСЯ: блоками по семь первый прогревает кеши для второго,
# и часть разницы оказывается временным дрейфом, а не свойством режима.
REPS="${REPS:-7}"
lease_ms=""; foll_ms=""; fr_prev="$(fr_total)"; grew_lease=0; grew_foll=0

for i in $(seq 1 "$REPS"); do
    a="$(psql_c -e "EXPLAIN ANALYZE SELECT count(*) FROM bulk_rows;" 2>&1 || true)"
    fr_mid="$(fr_total)"
    b="$(psql_c -e "EXPLAIN ANALYZE SELECT count(*) FROM bulk_rows AS OF SYSTEM TIME follower_read_timestamp();" 2>&1 || true)"
    fr_after="$(fr_total)"

    la="$(printf '%s\n' "$a" | awk '/execution time/ { print $3; exit }')"
    lb="$(printf '%s\n' "$b" | awk '/execution time/ { print $3; exit }')"
    hist="$(printf '%s\n' "$b" | grep -c 'historical:' || true)"
    d1=$(( fr_mid - fr_prev )); d2=$(( fr_after - fr_mid ))
    [ "$d1" -gt 0 ] && grew_lease=$(( grew_lease + 1 ))
    [ "$d2" -gt 0 ] && grew_foll=$(( grew_foll + 1 ))

    printf '  повтор %-2s  обычное %-8s follower %-8s  historical в плане: %s  прирост счётчика: обычное +%s, follower +%s\n' \
        "$i" "$la" "$lb" "$hist" "$d1" "$d2"
    lease_ms="$lease_ms $la"; foll_ms="$foll_ms $lb"; fr_prev="$fr_after"
done

log "обычное:   $lease_ms"
log "follower:  $foll_ms"
log "повторов, где счётчик вырос: на обычном чтении $grew_lease из $REPS, на follower $grew_foll из $REPS"

# Без прироста счётчика на follower-режиме вывод делать НЕЛЬЗЯ: значит,
# запрос обслуживал тот же арендатор, просто на старой метке.
[ "$grew_foll" -gt 0 ] \
    || fail "follower_reads_success_count не вырос ни разу — чтение обслуживал арендатор, сравнение недействительно"

log "сравнивать РАЗБРОС между повторами с разницей между режимами."
log "Вывод не предопределён: на одной машине близость реплики выигрыша не даёт,"
log "остаётся накладная стоимость механизма. Но у follower read есть и второй"
log "эффект — он снимает нагрузку с арендатора, — и сравнение latency одного"
log "запроса его не показывает вовсе."

echo
echo "=== C. Глубина AS OF SYSTEM TIME ==="
psql_c -e "SHOW ZONE CONFIGURATION FROM RANGE default;" | grep -i 'ttlseconds' || true

# ЛОВУШКА: отказ чтения в прошлом бывает по ДВУМ разным причинам, и
# спутать их легко. Либо снимок старше порога сборки мусора, либо
# таблицы в тот момент ещё не существовало. Второе меряет возраст
# стенда, а не свойство СУБД. Поэтому забираем текст ошибки, а не
# просто код возврата.
log "идём в прошлое, пока запрос не откажет:"
for back in '10s' '1m' '10m' '30m' '1h' '2h' '4h' '5h'; do
    err="$(psql_c -e "SELECT count(*) FROM bulk_rows AS OF SYSTEM TIME '-${back}';" 2>&1 >/dev/null || true)"
    if [ -z "$err" ]; then
        echo "  -${back}: ЧИТАЕТСЯ"
    elif printf '%s' "$err" | grep -qiE 'garbage collection|gc threshold|batch timestamp .* must be after'; then
        echo "  -${back}: ОТКАЗ — снимок за порогом сборки мусора (это и есть граница GC)"
    elif printf '%s' "$err" | grep -qiE 'does not exist|descriptor not found|relation .* undefined'; then
        echo "  -${back}: ОТКАЗ — таблицы в тот момент ещё не было (возраст стенда, а не GC)"
    else
        echo "  -${back}: ОТКАЗ — иная причина:"
        printf '%s\n' "$err" | grep -iE 'error' | head -1 | sed 's/^/      /'
    fi
done

echo
echo "=== C2. Порог сборки мусора: гарантия снизу, а не потолок ==="
#
# Проверяем ходовое заблуждение: «gc.ttlseconds = N значит, что глубже N
# читать нельзя». Опускаем порог до 30с на отдельной таблице, делаем
# мусор, ждём заведомо дольше порога — и смотрим, откажут ли снимок.
GC_WAIT="${GC_WAIT:-300}"

# Таблицу пересоздаём каждый прогон. Если оставить её от прошлого раза,
# порог сборки мусора уже уехал вперёд, и «чтение в прошлом» покажет
# состояние предыдущего запуска, а не свойство настройки.
psql_c -e "DROP TABLE IF EXISTS gc_probe;" >/dev/null
psql_c -e "CREATE TABLE gc_probe (id INT8 PRIMARY KEY, v STRING);" >/dev/null
psql_c -e "INSERT INTO gc_probe SELECT g, repeat(chr(97 + g % 26), 100)
           FROM generate_series(1, 20000) AS g;" >/dev/null
psql_c -e "ALTER TABLE gc_probe CONFIGURE ZONE USING gc.ttlseconds = 30;" >/dev/null
log "gc.ttlseconds для gc_probe опущен до 30с"

for i in 1 2 3 4 5; do
    psql_c -e "UPDATE gc_probe SET v = repeat(chr(97 + $i), 100);" >/dev/null
done
log "создано 5 поколений версий; ждём ${GC_WAIT}с — это в $((GC_WAIT / 30)) раз больше порога"
sleep "$GC_WAIT"

for back in '30s' '1m' '2m' '3m' '5m'; do
    err="$(psql_c -e "SELECT count(*) FROM gc_probe AS OF SYSTEM TIME '-${back}';" 2>&1 >/dev/null || true)"
    if [ -z "$err" ]; then
        echo "  -${back}: ЧИТАЕТСЯ (снимок старше порога, но версии ещё не собраны)"
    elif printf '%s' "$err" | grep -qiE 'garbage collection|gc threshold|must be after'; then
        echo "  -${back}: ОТКАЗ — порог GC уехал вперёд"
    else
        echo "  -${back}: ОТКАЗ — иная причина:"
        printf '%s\n' "$err" | grep -iE 'error' | head -1 | sed 's/^/      /'
    fi
done

log "ВЫВОД: gc.ttlseconds задаёт минимальную гарантию хранения версий,"
log "а не момент, с которого чтение в прошлом перестаёт работать. Реальный"
log "горизонт зависит от того, дошёл ли сборщик до этих диапазонов —"
log "он ходит по объёму мусора, а не по таймеру."

echo
echo "=== D. Uncertainty restarts при окне 2с ==="
u_before=0
for p in 18080 18081 18082; do
    v="$(curl -sf --max-time 5 "http://127.0.0.1:${p}/_status/vars" \
         | awk '/^txn_restarts_readwithinuncertainty/ { s += $2 } END { print s + 0 }')" \
        || fail "не удалось снять метрики с порта $p"
    u_before=$(( u_before + v ))
done
log "uncertainty restarts до прогона (сумма по узлам): $u_before"

# Писатель и читатель работают через РАЗНЫЕ узлы-шлюзы: если бы окно
# срабатывало, оно сработало бы именно здесь.
log "писатель на crdb1, читатель на crdb2, 30с…"
( cd loadgen && go run . -mode hot -workers 8 -duration 30s \
    -dsn 'postgresql://root@127.0.0.1:15436/bank?sslmode=disable' ) >/tmp/crdb-02-w.json &
writer=$!
( cd loadgen && go run . -mode read-hot -workers 8 -duration 30s \
    -dsn 'postgresql://root@127.0.0.1:15437/bank?sslmode=disable' ) >/tmp/crdb-02-r.json
wait "$writer"

echo "писатель: $(cat /tmp/crdb-02-w.json)"
echo "читатель: $(cat /tmp/crdb-02-r.json)"

u_after=0
for p in 18080 18081 18082; do
    v="$(curl -sf --max-time 5 "http://127.0.0.1:${p}/_status/vars" \
         | awk '/^txn_restarts_readwithinuncertainty/ { s += $2 } END { print s + 0 }')" \
        || fail "не удалось снять метрики с порта $p"
    u_after=$(( u_after + v ))
done
delta=$(( u_after - u_before ))
log "uncertainty restarts после прогона: $u_after (прирост: $delta)"

# Проверяем, что нагрузка вообще дошла: без этого «ноль ретраев» был бы
# неотличим от «ничего не выполнялось».
r_ops="$(awk -F'[:,]' '{ for (i = 1; i < NF; i++) if ($i ~ /"ops"/) print $(i+1) }' /tmp/crdb-02-r.json)"
[ "${r_ops:-0}" -gt 1000 ] || fail "читатель выполнил слишком мало операций (${r_ops:-0}) — вывод про ретраи несостоятелен"
log "читатель выполнил $r_ops операций — нагрузка состоялась"

if [ "$delta" -eq 0 ]; then
    log "РЕЗУЛЬТАТ: ноль uncertainty restarts при окне 2с и состоявшейся нагрузке."
    log "Причина не в нагрузке: узлы делят часы ядра хоста, расхождение равно нулю,"
    log "и попадать в окно неопределённости нечему. Подробности и опыт с faketime —"
    log "в FIXTURES.md, раздел отрицательных результатов."
else
    log "РЕЗУЛЬТАТ: прирост $delta — окно сработало. Разобрать, откуда взялось расхождение."
fi
