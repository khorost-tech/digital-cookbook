#!/usr/bin/env bash
#
# 03-txn.sh — артефакт к статье «Распределённые транзакции».
#
# Четыре замера:
#   A. однооператорная запись в горячую строку: contention виден ЗАДЕРЖКОЙ,
#      а не ретраями — конкуренты встают в очередь на ключе;
#   B. «прочитал — записал прочитанное»: здесь ретраи и появляются;
#   C. SERIALIZABLE против READ COMMITTED на той же нагрузке — и цена,
#      которую READ COMMITTED берёт за отсутствие ретраев (потерянные
#      обновления, считаются точно);
#   D. форма первичного ключа: монотонный против случайного.
#
# Работает на базовом профиле, ничего не гасит и не перезапускает.
#
# Запуск (из databases/cockroachdb/):  bash scripts/03-txn.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

require_cluster 3 120

OUT=/tmp/crdb-03
mkdir -p "$OUT"

log "собираем loadgen…"
( cd loadgen && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

# Сумма метрики по всем трём узлам: ретраи считаются на том узле, где
# выполнялась транзакция, и брать один узел означало бы потерять часть.
#
# ВАЖНО: это НАКОПИТЕЛЬНЫЙ счётчик с момента старта узла. Приписывать его
# абсолютное значение одному прогону нельзя — первая версия скрипта так и
# делала, и в статью уехало число, включавшее все предыдущие замеры.
# Пользоваться только через restarts_delta ниже.
restarts_total() { # $1 = хвост имени метрики, например writetooold
    local sum=0 p v
    for p in 18080 18081 18082; do
        v="$(curl -sf --max-time 5 "http://127.0.0.1:${p}/_status/vars" \
             | awk -v m="txn_restarts_$1" '$0 ~ "^" m "{" { s += $2 } END { print s + 0 }')" \
            || fail "не удалось снять метрики с порта $p"
        sum=$(( sum + v ))
    done
    printf '%s' "$sum"
}

jget() { # $1 = файл, $2 = ключ
    awk -F'[:,]' -v k="\"$2\"" '{ for (i = 1; i < NF; i++) if ($i ~ k) { gsub(/[^0-9.]/, "", $(i+1)); print $(i+1) } }' "$1"
}

echo "=== A. Однооператорная запись в горячую строку ==="
log "ожидание: ретраев нет, растёт задержка — конкуренты ждут на ключе"
for w in 1 4 16 32; do
    "$OUT/loadgen" -mode hot -workers "$w" -duration 30s > "$OUT/hot-$w.json"
    printf '  воркеров=%-3s ops=%-7s retries=%-6s p50=%-8s p99=%s\n' \
        "$w" "$(jget "$OUT/hot-$w.json" ops)" "$(jget "$OUT/hot-$w.json" retries)" \
        "$(jget "$OUT/hot-$w.json" p50_ms)" "$(jget "$OUT/hot-$w.json" p99_ms)"
done

METRICS="writetooold serializable txnpush txnaborted readwithinuncertainty"

# Снимок всех интересующих счётчиков разом.
#
# Сортировка обязательна: приросты считаются через join, а он требует
# отсортированного ввода. Без неё join молча не находит совпадений, и
# раздел с приростами выходит пустым — уже наступали.
snapshot_restarts() { # печатает строки «метрика значение», отсортированные
    local m
    for m in $METRICS; do printf '%s %s\n' "$m" "$(restarts_total "$m")"; done | sort
}

echo
echo "=== B. Прочитал — записал прочитанное (SERIALIZABLE) ==="
log "ожидание: те же воркеры, но теперь появляются ретраи"
log "серверные счётчики снимаются ДО и ПОСЛЕ каждого прогона — только прирост"
for w in 1 4 16 32; do
    snapshot_restarts > "$OUT/rmw-$w.before"
    "$OUT/loadgen" -mode hot-rmw -workers "$w" -duration 30s > "$OUT/rmw-$w.json" 2>"$OUT/rmw-$w.err"
    snapshot_restarts > "$OUT/rmw-$w.after"

    ops="$(jget "$OUT/rmw-$w.json" ops)"
    ret="$(jget "$OUT/rmw-$w.json" retries)"
    err="$(jget "$OUT/rmw-$w.json" errors)"
    printf '  воркеров=%-3s ops=%-7s клиентских retries=%-6s отвалилось=%-5s p50=%-8s p99=%s\n' \
        "$w" "$ops" "$ret" "$err" \
        "$(jget "$OUT/rmw-$w.json" p50_ms)" "$(jget "$OUT/rmw-$w.json" p99_ms)"
    printf '           прирост серверных счётчиков за ЭТОТ прогон:\n'
    join "$OUT/rmw-$w.before" "$OUT/rmw-$w.after" \
        | awk '{ d = $3 - $2; if (d != 0) printf "             txn.restarts.%-22s +%d\n", $1, d }'
done

echo
log "клиентский retries и серверные txn.restarts.* — РАЗНЫЕ величины и совпадать"
log "не обязаны: клиент считает попытки своего цикла, сервер — перезапуски"
log "транзакций, включая внутренние, которых клиент не видит."

echo
echo "=== C. SERIALIZABLE против READ COMMITTED: ретраи и потерянные обновления ==="
#
# Ключевая проверка. Счётчик увеличивается ровно на единицу за успешную
# транзакцию, поэтому итог ОБЯЗАН совпасть с числом успешных операций.
# Расхождение — это и есть потерянные обновления, цена слабой изоляции.
for iso in "serializable" "read committed"; do
    tag="$(printf '%s' "$iso" | tr ' ' '-')"
    psql_c -e "UPDATE hot_counter SET hits = 0 WHERE id = 1;" >/dev/null
    start="$(sql_value "SELECT hits FROM hot_counter WHERE id = 1;")"
    [ "$start" = "0" ] || fail "счётчик не обнулился перед замером (сейчас: $start)"

    "$OUT/loadgen" -mode hot-rmw -workers 16 -duration 30s -isolation "$iso" \
        > "$OUT/iso-$tag.json" 2>"$OUT/iso-$tag.err"

    ops="$(jget "$OUT/iso-$tag.json" ops)"
    ret="$(jget "$OUT/iso-$tag.json" retries)"
    err="$(jget "$OUT/iso-$tag.json" errors)"
    final="$(sql_value "SELECT hits FROM hot_counter WHERE id = 1;")"
    diff=$(( ops - final ))

    # Коды ошибок нужны, чтобы РАСХОЖДЕНИЕ можно было объяснить, а не
    # назвать. 40003 (statement_completion_unknown) — тот самый случай,
    # когда транзакция могла зафиксироваться, а клиент ответа не получил.
    # Первая версия скрипта объявляла неоднозначным коммитом любое
    # превышение счётчика над числом успехов, ничего не проверяя.
    codes="$(python3 - "$OUT/iso-$tag.json" <<'PY' 2>/dev/null || echo "н/д"
import json, sys
d = json.load(open(sys.argv[1]))
c = d.get("error_codes") or {}
print(", ".join(f"{k}:{v}" for k, v in sorted(c.items())) or "нет")
PY
)"
    amb="$(python3 - "$OUT/iso-$tag.json" <<'PY' 2>/dev/null || echo 0
import json, sys
print((json.load(open(sys.argv[1])).get("error_codes") or {}).get("40003", 0))
PY
)"

    if [ "$diff" -gt 0 ]; then
        verdict="ПОТЕРЯНО=$diff"
    elif [ "$diff" -lt 0 ]; then
        if [ "${amb:-0}" -gt 0 ]; then
            verdict="счётчик больше на $(( -diff )); неоднозначных коммитов (40003): $amb"
        else
            verdict="счётчик больше на $(( -diff )); 40003 НЕ зафиксирован — причина не установлена"
        fi
    else
        verdict="расхождения нет"
    fi

    printf '  %-16s ops=%-6s retries=%-6s отвалилось=%-5s счётчик=%-6s %s\n' \
        "$iso" "$ops" "$ret" "$err" "$final" "$verdict"
    printf '           коды ошибок: %s\n' "$codes"

    # Под SERIALIZABLE потерянных обновлений быть не может: счётчик обязан
    # быть НЕ МЕНЬШЕ числа подтверждённых транзакций. Превышение допустимо.
    if [ "$iso" = "serializable" ] && [ "$diff" -gt 0 ]; then
        fail "под SERIALIZABLE потеряно $diff обновлений — этого быть не может, замер неверен"
    fi
done

log "если под READ COMMITTED потеряно 0 — это НЕ подтверждение безопасности,"
log "а повод проверить, дошла ли конкуренция: сравнить с числом воркеров и ops."

echo
echo "=== D. Форма первичного ключа ==="
log "монотонный ключ загоняет вставки в последний диапазон, случайный разносит"
for mode in insert-seq insert-uuid; do
    "$OUT/loadgen" -mode "$mode" -workers 16 -duration 45s > "$OUT/$mode.json"
    printf '  %-12s ops=%-7s retries=%-6s p50=%-8s p95=%-8s p99=%s\n' \
        "$mode" "$(jget "$OUT/$mode.json" ops)" "$(jget "$OUT/$mode.json" retries)" \
        "$(jget "$OUT/$mode.json" p50_ms)" "$(jget "$OUT/$mode.json" p95_ms)" \
        "$(jget "$OUT/$mode.json" p99_ms)"
done

echo
echo "--- диапазоны после вставок:"
psql_c -e "SELECT 'accounts_seq' AS tbl, count(*) AS ranges FROM [SHOW RANGES FROM TABLE accounts_seq]
           UNION ALL
           SELECT 'accounts_uuid', count(*) FROM [SHOW RANGES FROM TABLE accounts_uuid];"

log "если у обеих таблиц по одному диапазону, распределять между узлами нечего."
log "ВНИМАНИЕ: этот замер НЕСТАБИЛЕН — в разных прогонах давал и паритет,"
log "и полуторакратную разницу в пользу UUID. Делать по нему вывод в любую"
log "сторону нельзя. Настоящее сравнение — в D2, на разделённых таблицах."

echo
echo "=== D2. Форма ключа на РАЗДЕЛЁННЫХ таблицах ==="
#
# Узкие строки не набирают минимальные 64 МиБ, поэтому естественного
# split не дождаться. Делим вручную: тогда у таблицы несколько диапазонов
# с разными арендаторами, и видно, размазывается нагрузка или бьёт в один.
#
# ВСЕ ТРИ ВАРИАНТА устроены одинаково: ключ приходит с клиента, INSERT
# один и тот же, параметров поровну. Прежняя версия давала монотонность
# серверным nextval() — и тогда «монотонный» вариант отличался не только
# порядком ключей, но и местом генерации, формой запроса, числом
# параметров и конкуренцией за последовательность. Разницу нельзя было
# приписать порядку.
psql_c -e "DROP TABLE IF EXISTS acct_mono;" >/dev/null
psql_c -e "DROP SEQUENCE IF EXISTS acct_mono_seq;" >/dev/null
psql_c -e "CREATE TABLE acct_mono (
               id INT8 PRIMARY KEY,
               owner STRING NOT NULL,
               balance DECIMAL(14,2) NOT NULL DEFAULT 0);" >/dev/null
split_tables() {
    psql_c -e "ALTER TABLE acct_mono SPLIT AT VALUES (250000), (500000), (750000);" >/dev/null
    psql_c -e "ALTER TABLE acct_randint SPLIT AT VALUES
                   (2305843009213693952),
                   (4611686018427387904),
                   (6917529027641081856);" >/dev/null
    psql_c -e "ALTER TABLE acct_rand SPLIT AT VALUES
                   ('40000000-0000-0000-0000-000000000000'),
                   ('80000000-0000-0000-0000-000000000000'),
                   ('c0000000-0000-0000-0000-000000000000');" >/dev/null
}

psql_c -e "DROP TABLE IF EXISTS acct_rand;" >/dev/null
psql_c -e "CREATE TABLE acct_rand (
               id UUID PRIMARY KEY,
               owner STRING NOT NULL,
               balance DECIMAL(14,2) NOT NULL DEFAULT 0);" >/dev/null
#
# КОНТРОЛЬНЫЙ ВАРИАНТ. В паре mono/rand разом меняются четыре вещи: порядок
# ключей, их тип, размер кодирования и генератор. Разницу в пропускной
# способности нельзя приписать монотонности, пока она не отделена от прочего.
# acct_randint — тот же INT8, что и acct_mono, но значения случайны по всему
# положительному диапазону. От acct_mono он отличается прежде всего
# распределением значений — но не только им: генератор здесь тоже свой.
psql_c -e "DROP TABLE IF EXISTS acct_randint;" >/dev/null
psql_c -e "CREATE TABLE acct_randint (
               id INT8 PRIMARY KEY,
               owner STRING NOT NULL,
               balance DECIMAL(14,2) NOT NULL DEFAULT 0);" >/dev/null
split_tables

for t in acct_mono acct_rand acct_randint; do
    n="$(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE $t];")"
    log "$t: диапазонов $n"
    [ "$n" -ge 4 ] || fail "$t не разделилась ($n диапазонов) — сравнение бессмысленно"
done

echo "--- арендаторы диапазонов до нагрузки:"
psql_c -e "SELECT 'acct_mono' AS tbl, range_id, lease_holder FROM [SHOW RANGES FROM TABLE acct_mono WITH DETAILS]
           UNION ALL
           SELECT 'acct_randint', range_id, lease_holder FROM [SHOW RANGES FROM TABLE acct_randint WITH DETAILS]
           UNION ALL
           SELECT 'acct_rand', range_id, lease_holder FROM [SHOW RANGES FROM TABLE acct_rand WITH DETAILS]
           ORDER BY 1, 2;"

#
# ПАРНЫЕ ЧЕРЕДУЮЩИЕСЯ СЕРИИ вместо одного прогона каждого режима.
#
# Один прогон в фиксированном порядке ничего не доказывает: разница может
# оказаться дрейфом машины, прогревом кешей или удачным размещением
# аренд. Поэтому режимы идут парами по раундам, а внутри раунда порядок
# инвертируется — так систематический дрейф гасится.
#
# Дополнительно снимаем нагрузку ПО УЗЛАМ: без неё вывод «два узла
# простаивали» остаётся домыслом, потому что раскладка ключей говорит
# лишь о том, куда легли строки, а не о том, кто выполнял работу.
ROUNDS="${ROUNDS:-3}"
DUR="${DUR:-45s}"   # столько же, сколько указано в статье

store_metric() { # $1 = имя метрики; печатает «store=значение» через пробел
    local p out
    for p in 18080 18081 18082; do
        out="$(curl -sf --max-time 5 "http://127.0.0.1:${p}/_status/vars" \
               | awk -v m="$1" '$0 ~ "^" m "[{]" {
                     match($0, /store="[0-9]+"/); s = substr($0, RSTART+7, RLENGTH-8)
                     printf "s%s=%.0f ", s, $2 }')" || true
        printf '%s' "$out"
    done
}

# Сброс таблиц ПЕРЕД КАЖДЫМ прогоном.
#
# Без него замер сравнивает не формы ключа, а состояния таблиц: строки
# копятся от раунда к раунду, и режим, отработавший последним, стартует на
# самой большой таблице. Первая версия так и была устроена — и монотонный
# вариант «деградировал» от раунда к раунду, хотя это могло быть просто
# накопление. Теперь каждый прогон стартует с пустых таблиц, а деградацию
# по мере роста меряет отдельный замер D3, где она и есть предмет.
reset_split_tables() {
    psql_c -e "TRUNCATE acct_mono;"    >/dev/null
    psql_c -e "TRUNCATE acct_randint;" >/dev/null
    psql_c -e "TRUNCATE acct_rand;"    >/dev/null
    # TRUNCATE в CockroachDB пересоздаёт таблицу, а с ней теряется ручное
    # разбиение — без него у таблицы снова один диапазон, и сравнение
    # «горячий хвост против размазанной нагрузки» разваливается.
    split_tables
}

run_one() { # $1 = mode (mono|rand), $2 = round
    #
    # ЛОВУШКА BASH: в «local a="$1" b="$a"» подстановка $a во втором
    # присваивании выполняется ДО того, как local присвоит первое, и
    # берётся старое ГЛОБАЛЬНОЕ значение. Из-за этого оба режима писали
    # результат в один файл, названный по переменной из предыдущего цикла.
    # Поэтому каждое присваивание — отдельной строкой.
    local mode="$1"
    local r="$2"
    local f="$OUT/split-$mode-r$r.json"
    local w_before w_after
    reset_split_tables
    #
    # Раскладка аренд снимается ВОКРУГ каждого прогона, а не один раз до
    # всего замера. TRUNCATE пересоздаёт таблицу вместе с диапазонами, так
    # что раскладка, снятая до первого сброса, к измеряемым диапазонам уже
    # не относится — а именно она и печаталась раньше.
    #
    # Утверждать по ней «нагрузка разошлась по узлам» всё равно нельзя:
    # балансировка аренд по нагрузке работает с интервалом около минуты,
    # а прогон длится 45 секунд. То есть за время замера аренды просто не
    # успевают переехать. Это ограничение постановки, и оно записано в
    # выводах: измеряется клиентская пропускная способность при
    # НЕКОНТРОЛИРУЕМОМ размещении аренд.
    printf '          аренды до прогона:  %s
' "$(leases_of "$mode")"
    w_before="$(store_metric rebalancing_writespersecond)"
    "$OUT/loadgen" -mode "split-$mode" -workers 16 -duration "$DUR" > "$f"
    w_after="$(store_metric rebalancing_writespersecond)"
    printf '    %-5s ops=%-7s p50=%-8s p95=%-8s\n' \
        "$mode" "$(jget "$f" ops)" "$(jget "$f" p50_ms)" "$(jget "$f" p95_ms)"
    printf '          записей/с по узлам в конце прогона: %s\n' "$w_after"
    printf '          нагрузка CPU по узлам (нс/с):       %s\n' "$(store_metric rebalancing_cpunanospersecond)"
    # Распределение строк снимаем ПРЯМО СЕЙЧАС, пока таблица не очищена
    # перед следующим прогоном. Раньше подсчёт стоял в конце всего замера
    # и показывал нули: последний TRUNCATE стирал свидетельство.
    printf '          строки по интервалам разбиения: %s\n' "$(rows_by_range "$mode")"
    printf '          аренды после прогона: %s
' "$(leases_of "$mode")"
}

leases_of() { # $1 = mode; печатает «диапазон:арендатор» через пробел
    local tbl
    case "$1" in
        mono)    tbl=acct_mono ;;
        randint) tbl=acct_randint ;;
        rand)    tbl=acct_rand ;;
    esac
    psql_c --format=tsv -e \
        "SELECT range_id, lease_holder FROM [SHOW RANGES FROM TABLE $tbl WITH DETAILS]
         ORDER BY range_id;" | tail -n +2 | awk '{ printf "%s:n%s ", $1, $2 }'
}

rows_by_range() { # $1 = mode; печатает четыре числа через табуляцию
    case "$1" in
        mono)
            psql_c --format=tsv -e "SELECT count(*) FILTER (WHERE id < 250000),
                count(*) FILTER (WHERE id >= 250000 AND id < 500000),
                count(*) FILTER (WHERE id >= 500000 AND id < 750000),
                count(*) FILTER (WHERE id >= 750000) FROM acct_mono;" | tail -1 ;;
        randint)
            psql_c --format=tsv -e "SELECT count(*) FILTER (WHERE id < 2305843009213693952),
                count(*) FILTER (WHERE id >= 2305843009213693952 AND id < 4611686018427387904),
                count(*) FILTER (WHERE id >= 4611686018427387904 AND id < 6917529027641081856),
                count(*) FILTER (WHERE id >= 6917529027641081856) FROM acct_randint;" | tail -1 ;;
        rand)
            psql_c --format=tsv -e "SELECT count(*) FILTER (WHERE id < '40000000-0000-0000-0000-000000000000'),
                count(*) FILTER (WHERE id >= '40000000-0000-0000-0000-000000000000'
                                   AND id < '80000000-0000-0000-0000-000000000000'),
                count(*) FILTER (WHERE id >= '80000000-0000-0000-0000-000000000000'
                                   AND id < 'c0000000-0000-0000-0000-000000000000'),
                count(*) FILTER (WHERE id >= 'c0000000-0000-0000-0000-000000000000') FROM acct_rand;" | tail -1 ;;
    esac
}

for r in $(seq 1 "$ROUNDS"); do
    echo "  --- раунд $r:"
    # Порядок трёх режимов циклически сдвигается по раундам: так ни один
    # не оказывается систематически первым, то есть на холодных кешах.
    case $(( r % 3 )) in
        1) run_one mono "$r";    run_one rand "$r";    run_one randint "$r" ;;
        2) run_one rand "$r";    run_one randint "$r"; run_one mono "$r" ;;
        0) run_one randint "$r"; run_one mono "$r";    run_one rand "$r" ;;
    esac
done

echo "  --- сводка по раундам (ops):"
for r in $(seq 1 "$ROUNDS"); do
    printf '    раунд %s: mono=%-8s randint=%-8s rand=%s\n' \
        "$r" "$(jget "$OUT/split-mono-r$r.json" ops)" \
        "$(jget "$OUT/split-randint-r$r.json" ops)" \
        "$(jget "$OUT/split-rand-r$r.json" ops)"
done
log "сравнивать надо ДВЕ пары: mono против randint (различаются порядком ключей)"
log "и randint против rand (различаются типом и размером). ИЗОЛЯЦИИ переменной тут"
log "нет: генераторы у всех трёх разные и работают внутри измеряемой транзакции —"
log "это сравнение трёх клиентских стратегий целиком."

echo "--- арендаторы диапазонов ПОСЛЕ нагрузки (сравнить с раскладкой до):"
psql_c -e "SELECT 'acct_mono' AS tbl, range_id, lease_holder FROM [SHOW RANGES FROM TABLE acct_mono WITH DETAILS]
           UNION ALL
           SELECT 'acct_randint', range_id, lease_holder FROM [SHOW RANGES FROM TABLE acct_randint WITH DETAILS]
           UNION ALL
           SELECT 'acct_rand', range_id, lease_holder FROM [SHOW RANGES FROM TABLE acct_rand WITH DETAILS]
           ORDER BY 1, 2;"

log "ГРАНИЦА ВЫВОДА. Измерена клиентская пропускная способность при"
log "неконтролируемом размещении аренд: балансировка по нагрузке работает с"
log "интервалом около минуты, прогон длится 45 секунд, нужной раскладки"
log "никто не дожидался. Горизонтальное масштабирование форм ключа этим"
log "замером НЕ проверено — для этого аренды надо развести и дождаться."

log "распределение строк печатается сразу после каждого прогона (см. выше):"
log "у монотонного ключа непустым должен быть ОДИН интервал, у обоих случайных — все четыре."

echo
echo "=== D3. Деградация по мере роста таблицы ==="
#
# Здесь предмет замера ДРУГОЙ, чем в D2. В D2 сравниваются формы ключа при
# равных условиях, поэтому таблицы сбрасываются перед каждым прогоном. А
# тут переменная — само накопление строк, и сброса между интервалами быть
# не должно.
#
# ПОРЯДОК ЧЕРЕДУЕТСЯ. Первая версия гоняла пять интервалов mono подряд, а
# потом пять интервалов randint. Такой замер показывает, что во ВТОРОЙ
# половине прогона пропускная способность ниже, но не отличает эффект
# ключа от дрейфа машины: к моменту второй серии кластер уже отработал
# полчаса. Теперь оба режима идут в одном интервале, а порядок внутри
# интервала инвертируется — обе таблицы растут в одинаковых условиях.
#
# Плюс ДВЕ серии с обратным стартовым порядком: если эффект от порядка, он
# в них поменяет знак; если от формы ключа — повторится.
DECAY_INTERVALS="${DECAY_INTERVALS:-5}"

decay_run() { # $1 = mode, $2 = серия, $3 = интервал
    local f="$OUT/decay-$2-$1-$3.json"
    "$OUT/loadgen" -mode "split-$1" -workers 16 -duration "$DUR" > "$f"
    printf '%s' "$(jget "$f" ops)"
}

for series in 1 2; do
    reset_split_tables
    echo "  --- серия $series (стартует $( [ "$series" = 1 ] && echo mono || echo randint )):"
    printf '    %-10s %-10s %-10s
' "интервал" "mono" "randint"
    for i in $(seq 1 "$DECAY_INTERVALS"); do
        # Порядок внутри интервала инвертируется, стартовый режим серии
        # тоже разный — так ни один режим систематически не оказывается
        # ни первым, ни последним.
        if [ $(( (i + series) % 2 )) -eq 0 ]; then
            m="$(decay_run mono "$series" "$i")"
            r="$(decay_run randint "$series" "$i")"
        else
            r="$(decay_run randint "$series" "$i")"
            m="$(decay_run mono "$series" "$i")"
        fi
        printf '    %-10s %-10s %-10s
' "$i" "$m" "$r"
    done
    log "серия $series: acct_mono $(sql_value "SELECT count(*) FROM acct_mono;") строк / $(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE acct_mono];") диапазонов; acct_randint $(sql_value "SELECT count(*) FROM acct_randint;") / $(sql_value "SELECT count(*) FROM [SHOW RANGES FROM TABLE acct_randint];")"
done

log "вывод о ПРИЧИНЕ делать только если обе серии дали одинаковое направление:"
log "иначе это дрейф машины или эффект порядка, а не форма ключа."

echo "=== Постусловие ==="
seq_n="$(sql_value "SELECT count(*) FROM accounts_seq;")"
uuid_n="$(sql_value "SELECT count(*) FROM accounts_uuid;")"
log "строк: accounts_seq=$seq_n accounts_uuid=$uuid_n"
[ "$seq_n" -gt 0 ] && [ "$uuid_n" -gt 0 ] || fail "вставки не дошли до кластера — замер D несостоятелен"
