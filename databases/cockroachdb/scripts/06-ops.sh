#!/usr/bin/env bash
#
# 06-ops.sh — артефакт к статье «Эксплуатация и миграция с PostgreSQL».
#
# Пять замеров:
#   A. лицензия и телеметрия на живом кластере;
#   B. миграция PG-схемы: что переносится, а что нет — по одному оператору;
#   C. чужой драйвер: JDBC подключается, retry-цикл пишется руками;
#   D. бэкап и восстановление со сверкой числа строк;
#   E. rolling upgrade 25.4 -> 26.2 с сосуществованием версий.
#
# Работает на ОТДЕЛЬНОМ кластере (проект crdbops, свои тома), базовый не
# трогает. Базовый останавливается на время прогона, чтобы шесть узлов не
# толкались на одной машине.
#
# Запуск (из databases/cockroachdb/):  bash scripts/06-ops.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OPS_COMPOSE="compose/ops.yml"
PRIMARY_NODE=ops1
DB_NAME=bank

OLD_IMAGE="cockroachdb/cockroach:v25.4.14"
NEW_IMAGE="cockroachdb/cockroach:v26.2.5"

export CRDB_OPS_IMAGE_1="$OLD_IMAGE"
export CRDB_OPS_IMAGE_2="$OLD_IMAGE"
export CRDB_OPS_IMAGE_3="$OLD_IMAGE"

cleanup() {
    log "снимаем ops-кластер и возвращаем базовый…"
    docker compose -f "$OPS_COMPOSE" down -v --remove-orphans >/dev/null 2>&1 || true
    docker compose -f compose/compose.yml start >/dev/null 2>&1 || true
}
trap cleanup EXIT

log "останавливаем базовый кластер на время прогона…"
docker compose -f compose/compose.yml stop >/dev/null 2>&1 || true

log "поднимаем три узла на $OLD_IMAGE…"
docker compose -f "$OPS_COMPOSE" up -d >/dev/null

log "инициализация…"
initialized=0
for _ in $(seq 1 40); do
    out="$(docker exec ops1 cockroach init --insecure --host=localhost:26257 2>&1 || true)"
    if printf '%s' "$out" | grep -qE 'successfully initialized|already been initialized'; then
        initialized=1; break
    fi
    sleep 3
done
[ "$initialized" -eq 1 ] || fail "ops-кластер не инициализировался"

for n in ops1 ops2 ops3; do wait_healthy "$n" 60; done
[ "$(nodes_live)" -eq 3 ] || fail "ожидали 3 живых узла"

docker exec -i ops1 cockroach sql --insecure --host=localhost:26257 < sql/01-schema.sql >/dev/null
log "кластер готов, схема накатана"

echo
echo "=== A. Лицензия и телеметрия ==="
# «|| true» здесь не косметика: под set -e -o pipefail конвейер в head
# возвращает ненулевой код из-за SIGPIPE, и скрипт падал бы на выводе.
docker exec ops1 cockroach version --format=tsv 2>/dev/null | head -2 || true
psql_c -e "SELECT variable, value FROM [SHOW ALL CLUSTER SETTINGS]
           WHERE variable IN ('enterprise.license', 'diagnostics.reporting.enabled',
                              'diagnostics.reporting.interval', 'cluster.organization');"
log "пустой enterprise.license означает, что кластер живёт в 7-дневном грейсе"
log "diagnostics.reporting.enabled = true — телеметрия уходит наружу, на бесплатной лицензии её не отключить"
psql_c -e "SELECT now() AS сейчас;"

echo
echo "=== B. Миграция PG-схемы: по одному оператору ==="
#
# Не прерываемся на первой ошибке: список отличий и есть результат.
# Каждый оператор применяется отдельно, вывод классифицируется.
#
# ЛОВУШКА, на которую я уже наступил: читать файл через «while read» и
# внутри цикла звать psql_c нельзя. psql_c — это docker exec -i, он
# забирает stdin себе и съедает остаток файла; цикл после первой же
# итерации остаётся без входных данных и молча заканчивается. Поэтому
# сначала режем файл на отдельные операторы, и только потом выполняем.
SPLIT_DIR="$(mktemp -d)"
#
# ВТОРАЯ ловушка, тоже своя: разделителем сперва была строка «-- @@», и
# она встречалась ВНУТРИ комментария, который её описывал. awk разрезал
# файл по объяснению, CREATE DATABASE не выполнился, и дюжина операторов
# отказала с «database does not exist» — то есть замер показывал бы мою
# ошибку разбора вместо несовместимости.
#
# ТРЕТЬЯ ловушка: обход массива через «for (i in lines)» в awk идёт в
# НЕОПРЕДЕЛЁННОМ порядке. На многострочном операторе это перемешивает
# строки, и на выполнение уходит синтаксический мусор. Обходим по индексу.
#
# Заодно вытаскиваем ожидаемый исход из строки «-- EXPECT: pass|fail»,
# чтобы сверять каждый оператор отдельно, а не считать общее число отказов.
#
# Переменная называется want, а не exp: exp — встроенная функция awk
# (экспонента), и присваивание ей даёт syntax error.
awk -v d="$SPLIT_DIR" '
    BEGIN { RS = "-- ##SPLIT##"; n = 0 }
    {
        s = ""; want = "?"
        cnt = split($0, lines, "\n")
        for (i = 1; i <= cnt; i++) {
            if (lines[i] ~ /^[ \t]*-- EXPECT:[ \t]*(pass|fail)[ \t]*$/) {
                want = lines[i]
                sub(/^[ \t]*-- EXPECT:[ \t]*/, "", want)
                sub(/[ \t]*$/, "", want)
                continue
            }
            if (lines[i] ~ /^[ \t]*--/) continue
            s = s lines[i] "\n"
        }
        gsub(/^[ \t\r\n]+/, "", s); gsub(/[ \t\r\n]+$/, "", s)
        if (length(s) > 0) {
            n++
            printf "%s", s   > sprintf("%s/%03d.sql", d, n)
            printf "%s", want > sprintf("%s/%03d.expect", d, n)
        }
    }
' sql/06-pg-schema.sql

ok_n=0; fail_n=0; idx=0; mismatch=0
for f in "$SPLIT_DIR"/*.sql; do
    idx=$((idx + 1))
    stmt="$(cat "$f")"
    want="$(cat "${f%.sql}.expect" 2>/dev/null || echo '?')"
    first="$(printf '%s' "$stmt" | tr '\n' ' ' | sed 's/  */ /g' | cut -c1-58)"

    # Первый оператор создаёт базу shop, поэтому идёт в defaultdb;
    # остальные выполняются УЖЕ ВНУТРИ shop. Иначе функция и триггер
    # отказывали с «cross-db references not supported» — то есть замер
    # показывал бы мою ошибку подключения, а не ограничение СУБД.
    if [ "$idx" -eq 1 ]; then db=defaultdb; else db=shop; fi

    # Вердикт по КОДУ ВОЗВРАТА, а не по пустоте stderr: CockroachDB
    # пишет туда NOTICE даже при успехе, и проверка на пустоту объявляла
    # успешные CREATE TABLE отказавшими.
    if out="$(docker exec -i ops1 cockroach sql --insecure --host=localhost:26257 \
                --database="$db" -e "$stmt;" 2>&1)"; then
        got=pass
        printf '  ПРОШЛО   %s…\n' "$first"
        note="$(printf '%s' "$out" | grep -iE '^NOTICE' | head -1 | cut -c1-100 || true)"
        [ -n "$note" ] && printf '           (%s)\n' "$note"
        ok_n=$((ok_n + 1))
    else
        got=fail
        reason="$(printf '%s' "$out" | grep -iE '^ERROR' | head -1 | cut -c1-110 || true)"
        printf '  ОТКАЗ    %s…\n           %s\n' "$first" "${reason:-без текста ошибки}"
        fail_n=$((fail_n + 1))
    fi

    if [ "$want" = "?" ]; then
        printf '           ОЖИДАНИЕ НЕ ЗАДАНО — добавьте «-- EXPECT:» в sql/06-pg-schema.sql\n'
        mismatch=$((mismatch + 1))
    elif [ "$want" != "$got" ]; then
        printf '           РАСХОЖДЕНИЕ: ожидалось %s, получено %s\n' "$want" "$got"
        mismatch=$((mismatch + 1))
    fi
done
rm -rf "$SPLIT_DIR"

log "прошло: $ok_n, отказало: $fail_n"
# Общего числа отказов НЕДОСТАТОЧНО: оно совпадёт и тогда, когда
# результаты поменялись местами. Сверяем каждый оператор со своим
# ожиданием.
[ "$mismatch" -eq 0 ] \
    || fail "расхождений с ожиданиями: $mismatch — матрица совместимости изменилась, разобрать построчно"
log "все операторы дали ожидаемый исход"
echo "--- во что превратился SERIAL:"
psql_c -e "SHOW CREATE TABLE shop.public.customers;" 2>&1 | head -8 || true

echo
echo "=== C. Чужой драйвер: JDBC ==="
#
# Java-программа работает В ОДИН поток. Без посторонней конкуренции
# ретраев не будет вовсе, и «0 повторов» читалось бы как «на JDBC
# ретраи не нужны» — вывод неверный. Поэтому параллельно бьём в тот же
# ключ Go-нагрузкой: тогда видно, что retry-цикл на JDBC приходится
# писать руками и он реально срабатывает.
OUT=/tmp/crdb-06
mkdir -p "$OUT"
( cd loadgen && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

psql_c -e "CREATE TABLE IF NOT EXISTS jdbc_counter (id INT8 PRIMARY KEY, hits INT8 NOT NULL);" >/dev/null
psql_c -e "UPSERT INTO jdbc_counter (id, hits) VALUES (1, 0);" >/dev/null

log "фоновая конкуренция на том же ключе…"
"$OUT/loadgen" -mode jdbc-rmw -workers 8 -duration 60s \
    -dsn "postgresql://root@127.0.0.1:15450/bank?sslmode=disable" > "$OUT/jdbc-bg.json" 2>/dev/null &
bg_pid=$!
sleep 2

docker run --rm --network host \
    -v "$HERE/javacheck:/app" -w /app \
    maven:3-eclipse-temurin-21 \
    mvn -q compile exec:java \
        -Dexec.args="jdbc:postgresql://127.0.0.1:15450/bank?sslmode=disable root 200" \
    || { kill "$bg_pid" 2>/dev/null || true; fail "JDBC-проверка не отработала"; }

wait "$bg_pid" || true
log "фоновая нагрузка (Go): $(cat "$OUT/jdbc-bg.json")"

echo
echo "=== D. Бэкап и восстановление ==="
rows_before="$(sql_value "SELECT count(*) FROM accounts_uuid;")"
if [ "$rows_before" -lt 1000 ]; then
    psql_c -e "INSERT INTO accounts_uuid (owner, balance)
               SELECT 'seed-' || g, g FROM generate_series(1, 5000) AS g;" >/dev/null
    rows_before="$(sql_value "SELECT count(*) FROM accounts_uuid;")"
fi
log "строк в accounts_uuid до бэкапа: $rows_before"

# userfile не требует настройки внешнего хранилища — бэкап ложится
# внутрь самого кластера. Для стенда это то, что нужно.
backup_start="$(date +%s)"
psql_c -e "BACKUP DATABASE bank INTO 'userfile://defaultdb.public.userfiles_root/backup';" >/dev/null
backup_elapsed=$(( $(date +%s) - backup_start ))
log "бэкап занял ${backup_elapsed}с"
psql_c -e "SHOW BACKUPS IN 'userfile://defaultdb.public.userfiles_root/backup';"

log "роняем таблицу и восстанавливаем базу из бэкапа…"
psql_c -e "DROP TABLE accounts_uuid;" >/dev/null
dropped="$(psql_c -e "SELECT count(*) FROM accounts_uuid;" 2>&1 | grep -ciE 'does not exist|undefined' || true)"
[ "$dropped" -ge 1 ] || fail "таблица не удалилась — восстанавливать нечего, замер был бы фиктивным"

restore_start="$(date +%s)"
psql_c -e "RESTORE TABLE bank.public.accounts_uuid
           FROM LATEST IN 'userfile://defaultdb.public.userfiles_root/backup';" >/dev/null
restore_elapsed=$(( $(date +%s) - restore_start ))
rows_after="$(sql_value "SELECT count(*) FROM accounts_uuid;")"
log "восстановление заняло ${restore_elapsed}с, строк: $rows_after"
[ "$rows_after" = "$rows_before" ] \
    || fail "после восстановления $rows_after строк вместо $rows_before — замер провален"
log "число строк совпало до единицы: $rows_after"

echo
echo "=== E. Rolling upgrade $OLD_IMAGE -> $NEW_IMAGE ==="

# Автофинализация включена по умолчанию и стартует, как только последний
# узел присоединился на новой версии. Без её отключения «старая версия
# кластера» после обновления — это не контролируемое окно отката, а
# промежуточное состояние, которое вот-вот закончится само.
log "отключаем автофинализацию, чтобы окно отката было настоящим:"
if psql_c -e "SET CLUSTER SETTING cluster.auto_upgrade.enabled = false;" >/dev/null 2>&1; then
    psql_c -e "SELECT variable, value FROM [SHOW ALL CLUSTER SETTINGS]
               WHERE variable = 'cluster.auto_upgrade.enabled';"
else
    log "ВНИМАНИЕ: настройка cluster.auto_upgrade.enabled не принята этой версией."
    log "Значит, увиденная ниже старая версия кластера может быть промежуточным"
    log "состоянием автофинализации, а не окном отката. Так и записывать."
fi

psql_c -e "SHOW CLUSTER SETTING version;"

# Непрерывный клиентский пробник. Без него «простоя не было» — это не
# измерение, а пересказ того, что контейнеры стали healthy. Пробник
# бьёт в кластер всё время обновления и фиксирует каждый разрыв.
UP_STOP=/tmp/crdb-06-stop
UP_LOG=/tmp/crdb-06-probe.log
rm -f "$UP_STOP" "$UP_LOG"

upgrade_probe() {
    while [ ! -f "$UP_STOP" ]; do
        if timeout 10 docker exec -i ops1 cockroach sql --insecure \
             --host=localhost:26257 --database=bank \
             -e "SELECT count(*) FROM accounts_uuid;" >/dev/null 2>&1; then
            echo "$(date +%s.%N) ok" >> "$UP_LOG"
        else
            echo "$(date +%s.%N) fail" >> "$UP_LOG"
        fi
    done
}

upgrade_probe &
UP_PID=$!
log "базовая линия пробника 15с…"
sleep 15

upgrade_start="$(date +%s)"
for pair in "1 ops1" "2 ops2" "3 ops3"; do
    set -- $pair
    idx="$1"; name="$2"
    log "обновляем $name…"
    export "CRDB_OPS_IMAGE_${idx}=$NEW_IMAGE"
    docker compose -f "$OPS_COMPOSE" up -d --no-deps "$name" >/dev/null
    wait_healthy "$name" 90
    live="$(nodes_live)"
    [ "$live" -eq 3 ] || fail "после обновления $name живых узлов: $live"
    echo "  версии узлов сейчас:"
    docker exec ops1 cockroach node status --insecure --host=localhost:26257 --format=tsv 2>/dev/null \
        | awk -F'\t' 'NR == 1 { next } { print "    " $2 " -> " $4 }'
done
upgrade_elapsed=$(( $(date +%s) - upgrade_start ))

sleep 20
touch "$UP_STOP"
wait "$UP_PID" || true

samples="$(wc -l < "$UP_LOG")"
oks="$(grep -c ' ok$' "$UP_LOG" || true)"
fails="$(grep -c ' fail$' "$UP_LOG" || true)"
log "rolling upgrade занял ${upgrade_elapsed}с"
log "проб за время обновления: $samples (успешных: $oks, неуспешных: $fails)"
[ "$samples" -ge 30 ] || fail "проб слишком мало ($samples) — про доступность вывод делать нельзя"

# Пробник бьёт в ops1 — тот самый узел, который тоже перезапускается.
# Разрыв в его обслуживании и есть то, что увидел бы клиент, ходящий в
# один узел без балансировщика.
awk '$2 == "ok" { if (prev != "") { d = $1 - prev; if (d > max) max = d } prev = $1 }
     END { printf "  максимальный разрыв между успешными запросами: %.1f с\n", max }' "$UP_LOG"
awk '$2 == "fail" { c++ } END { printf "  проб с явной ошибкой: %d\n", c + 0 }' "$UP_LOG"

log "версия кластера ДО финализации (двоичные уже новые):"
psql_c -e "SHOW CLUSTER SETTING version;"
psql_c -e "SET CLUSTER SETTING version = crdb_internal.node_executable_version();" >/dev/null
sleep 10
log "версия кластера ПОСЛЕ финализации:"
psql_c -e "SHOW CLUSTER SETTING version;"

echo
echo "=== Постусловие ==="
final_rows="$(sql_value "SELECT count(*) FROM accounts_uuid;")"
[ "$final_rows" = "$rows_before" ] \
    || fail "после апгрейда $final_rows строк вместо $rows_before — данные пострадали"
[ "$(nodes_live)" -eq 3 ] || fail "после апгрейда живы не все узлы"
log "все три узла живы, данные на месте ($final_rows строк)"
