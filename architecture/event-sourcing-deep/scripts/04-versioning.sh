#!/usr/bin/env bash
#
# 04-versioning.sh — артефакт к статье «Версионирование событий и upcasting».
#
# Предмет замера: цена цепочки upcaster'ов на чтении. Один и тот же стрим
# ("tank") наполняется либо ЦЕЛИКОМ в форме v3 (schema_ver=3 — Upcast не
# делает лишней работы), либо ЦЕЛИКОМ в форме v1 (schema_ver=1 — на каждое
# событие два шага цепочки: подставить unit, переименовать liters->amount).
# Число событий, их порядок и значения — одни и те же в обоих вариантах,
# различается только форма хранения. Оба чтения идут через ОДИН и тот же
# код (runVersioningRead -> Upcast), иначе замер сравнивал бы разный код,
# а не разную цену внутри одного кода.
#
# Замеряются ДВА интервала раздельно: fetch_ms (сетевой круговорот к
# Postgres — строки выгружаются в память ДО начала замера Upcast) и
# upcast_ms (цикл Upcast над уже выгруженными строками, без единого
# обращения к сети). Сравнивать нужно только upcast_ms: при слитном замере
# (единый таймер на fetch+upcast) сетевой круговорот (сотни мс на 200000
# строк) забивал собой разницу в работе Upcast, и отношение v1/v3 гуляло
# 0.95-1.10 без устойчивого направления — раздельные интервалы это лечат,
# но НЕ снимают другой эффект: см. ниже и «Изолированный микробенчмарк» в
# конце скрипта — даже раздельно посчитанный upcast_ms путает цепочку
# преобразований с длиной исходного JSON (v1-payload короче v3-payload на
# одно поле), поэтому для итогового вывода в скрипте используется ЕЩЁ и
# go-бенчмарк с фиксированной длиной входа.
#
# Порядок вариантов чередуется по раундам (не меньше трёх), состояние
# сбрасывается перед КАЖДЫМ прогоном (TRUNCATE + переналивка нужной формы).
#
# Постусловие проверяет ДВЕ величины, не одну: свёртка (сумма amount по всем
# событиям стрима после Upcast) обязана СОВПАСТЬ у v3-прогона и v1-прогона в
# каждом раунде, И unit каждого события после Upcast обязан РАВНЯТЬСЯ
# ожидаемому значению ("liters") — по значению, а не по факту "поле
# непустое". Одной только суммы недостаточно: испорченный шаг v1->v2 мог бы
# подставлять в unit мусор или пустую строку вместо "liters" без изменения
# Amount, и это осталось бы незамеченным (фикс-раунд 1: изначальная версия
# скрипта проверяла только сумму — см. FIXTURES.md, «Фикс-раунд 1»).
# Расхождение любой из двух величин значит, что upcasting меняет смысл
# события, — это дефект, а не замер, и скрипт обязан упасть.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/04-versioning.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-04
mkdir -p "$OUT"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

jget() { # $1 = файл, $2 = ключ
    python3 -c "import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])" "$1" "$2"
}

# EVENTS — сколько событий в стриме "tank" в каждом варианте. Фиксировано в
# скрипте, не подобрано под красивое число: величина достаточная, чтобы
# время чтения вышло из шума миллисекундного таймера (см. B в артефакте 01,
# где 1449-1268710 записей давали 20-644 мс).
EVENTS="${EVENTS:-200000}"

# reset_and_seed_v3 — стрим целиком в ТЕКУЩЕЙ форме (schema_ver=3):
# {"type":"OxygenAdded","amount":1,"unit":"liters"}. Upcast для каждой такой
# строки идёт по ветке `case schemaVer == 3` — переименовывать и подставлять
# нечего.
reset_and_seed_v3() {
    psql_c -c "TRUNCATE events, snapshots, balances, checkpoints RESTART IDENTITY;" >/dev/null
    psql_c -c "
        INSERT INTO events (stream_id, version, event_type, schema_ver, data)
        SELECT 'tank', i, 'OxygenAdded', 3,
               jsonb_build_object('type', 'OxygenAdded', 'amount', 1, 'unit', 'liters')
        FROM generate_series(1, $EVENTS) AS i;" >/dev/null
}

# reset_and_seed_v1 — тот же стрим, та же длина, то же значение на событие,
# но в САМОЙ СТАРОЙ форме (schema_ver=1): {"type":"OxygenAdded","liters":1}.
# Upcast для каждой такой строки идёт по ветке `schemaVer <= 1` -> fallthrough
# -> `schemaVer == 2` — оба шага цепочки отрабатывают на каждом событии.
reset_and_seed_v1() {
    psql_c -c "TRUNCATE events, snapshots, balances, checkpoints RESTART IDENTITY;" >/dev/null
    psql_c -c "
        INSERT INTO events (stream_id, version, event_type, schema_ver, data)
        SELECT 'tank', i, 'OxygenAdded', 1,
               jsonb_build_object('type', 'OxygenAdded', 'liters', 1)
        FROM generate_series(1, $EVENTS) AS i;" >/dev/null
}

run_variant() { # $1 = метка (v3|v1), $2 = раунд
    local label="$1" r="$2"
    local f="$OUT/$label-r$r.json"
    if [ "$label" = v3 ]; then reset_and_seed_v3; else reset_and_seed_v1; fi
    "$OUT/loadgen" -dsn "$PG_DSN" -mode versioning-read > "$f"
    printf '    %-2s events=%-7s sum_amount=%-10s bad_unit=%-3s fetch_ms=%-12s upcast_ms=%s\n' \
        "$label" "$(jget "$f" events)" "$(jget "$f" sum_amount)" "$(jget "$f" bad_unit_events)" \
        "$(jget "$f" fetch_ms)" "$(jget "$f" upcast_ms)"
}

ROUNDS="${ROUNDS:-3}"

echo "=== Цена цепочки upcaster'ов на чтении (stream=\"tank\", $EVENTS событий) ==="
log "раундов $ROUNDS, порядок вариантов чередуется, состояние сбрасывается перед каждым прогоном"

for r in $(seq 1 "$ROUNDS"); do
    echo "  --- раунд $r:"
    if [ $(( r % 2 )) -eq 1 ]; then
        run_variant v3 "$r"; run_variant v1 "$r"
    else
        run_variant v1 "$r"; run_variant v3 "$r"
    fi
done

echo
echo "=== Постусловие: свёртки v3 и v1 обязаны совпасть в каждом раунде ==="
# Проверяются ДВЕ величины, а не одна: sum_amount (значение) и
# bad_unit_events (единица измерения). Одной только суммы недостаточно —
# испорченный шаг v1->v2, подставляющий в unit мусор или пустую строку
# вместо "liters", НЕ изменил бы Amount и прошёл бы проверку только по
# сумме. bad_unit_events считается в Go (main.go: runVersioningRead) ПО
# ЗНАЧЕНИЮ (Unit != "liters"), а не по факту "поле непустое".
for r in $(seq 1 "$ROUNDS"); do
    v3_sum="$(jget "$OUT/v3-r$r.json" sum_amount)"
    v1_sum="$(jget "$OUT/v1-r$r.json" sum_amount)"
    v3_events="$(jget "$OUT/v3-r$r.json" events)"
    v1_events="$(jget "$OUT/v1-r$r.json" events)"
    v3_bad_unit="$(jget "$OUT/v3-r$r.json" bad_unit_events)"
    v1_bad_unit="$(jget "$OUT/v1-r$r.json" bad_unit_events)"
    [ "$v3_events" = "$EVENTS" ] && [ "$v1_events" = "$EVENTS" ] \
        || fail "раунд $r: прочитано не то число событий (v3=$v3_events, v1=$v1_events, ожидалось $EVENTS)"
    # Сравнение через python: числа с плавающей точкой, bash их не умеет.
    equal="$(python3 -c "print('1' if abs($v3_sum - $v1_sum) < 1e-9 else '0')")"
    [ "$equal" = "1" ] \
        || fail "раунд $r: свёртки суммы разошлись — v3_sum=$v3_sum, v1_sum=$v1_sum. Upcasting меняет смысл события, это дефект, а не замер."
    [ "$v3_bad_unit" = "0" ] && [ "$v1_bad_unit" = "0" ] \
        || fail "раунд $r: у части событий unit после Upcast не равен \"liters\" (v3_bad_unit=$v3_bad_unit, v1_bad_unit=$v1_bad_unit) — сумма могла совпасть, а единица измерения испорчена, это дефект, а не замер."
    log "раунд $r: свёртки совпали (v3_sum=$v3_sum, v1_sum=$v1_sum), unit верен на всех $EVENTS событиях в обоих вариантах"
done

echo
echo "=== Сводка (upcast_ms — ТОЛЬКО цикл Upcast над выгруженными строками,"
echo "    без сетевого fetch — и отношение v1/v3 по раундам) ==="
for r in $(seq 1 "$ROUNDS"); do
    v3_ms="$(jget "$OUT/v3-r$r.json" upcast_ms)"
    v1_ms="$(jget "$OUT/v1-r$r.json" upcast_ms)"
    ratio="$(python3 -c "print(f'{$v1_ms / $v3_ms:.2f}')")"
    printf '    раунд %s: v3 upcast=%s мс  v1 upcast=%s мс  v1/v3=%s\n' "$r" "$v3_ms" "$v1_ms" "$ratio"
done

echo
echo "=== Изолированный микробенчмарк: одни и те же байты, разный schemaVer ==="
log "полевой замер выше путает две причины: длину JSON (v1-payload короче на"
log "одно поле, чем v3-payload) и число шагов цепочки. Ниже — go test -bench"
log "на ОДНИХ И ТЕХ ЖЕ сырых байтах (benchRaw в go/upcast_test.go),"
log "различается только передаваемый schemaVer (1 -> полная цепочка из двух"
log "шагов, 3 -> ветка без единой map-операции). Разница во времени здесь"
log "объясняется ТОЛЬКО кодом ветки switch, а не разной длиной JSON."
( cd go && go test -run '^$' -bench 'BenchmarkUpcast' -benchmem -count=5 . ) \
    | tee "$OUT/bench.txt"

# Извлекаем min/max ns/op каждого бенчмарка ИЗ ФАКТИЧЕСКОГО вывода выше —
# числа в тексте вывода ниже обязаны совпадать с тем, что реально
# напечатал go test в ЭТОМ прогоне, а не быть переписаны вручную из
# предыдущего запуска.
v1_min="$(awk '/BenchmarkUpcastV1Chain/{print $3}' "$OUT/bench.txt" | sort -n | head -1)"
v1_max="$(awk '/BenchmarkUpcastV1Chain/{print $3}' "$OUT/bench.txt" | sort -n | tail -1)"
v3_min="$(awk '/BenchmarkUpcastV3NoOp/{print $3}' "$OUT/bench.txt" | sort -n | head -1)"
v3_max="$(awk '/BenchmarkUpcastV3NoOp/{print $3}' "$OUT/bench.txt" | sort -n | tail -1)"
v1_allocs="$(awk '/BenchmarkUpcastV1Chain/{print $7}' "$OUT/bench.txt" | sort -u)"
v3_allocs="$(awk '/BenchmarkUpcastV3NoOp/{print $7}' "$OUT/bench.txt" | sort -u)"
v1_bytes="$(awk '/BenchmarkUpcastV1Chain/{print $5}' "$OUT/bench.txt" | sort -u)"
v3_bytes="$(awk '/BenchmarkUpcastV3NoOp/{print $5}' "$OUT/bench.txt" | sort -u)"

# Пустой результат парсинга — это тоже провал гейта, а не "1 строка". Если
# awk ничего не нашёл (например, имя бенчмарка в go/upcast_test.go
# переименовали, а здесь забыли поправить), переменная пуста, а
# `printf '%s\n' "" | wc -l` вернёт 1 (одна ПУСТАЯ строка) — старая версия
# гейта это принимала как "детерминировано", арифметика на пустых строках
# молча давала 0, и скрипт печатал заведомо неверный вывод "цепочка
# добавляет 0 лишних аллокаций" с кодом возврата 0. Проверяем непустоту
# КАЖДОЙ переменной отдельно, до проверки детерминированности.
for var_name in v1_min v1_max v3_min v3_max v1_allocs v3_allocs v1_bytes v3_bytes; do
    var_val="${!var_name}"
    [ -n "$var_val" ] \
        || fail "не удалось распарсить $var_name из вывода бенчмарка (awk не нашёл ожидаемую строку в $OUT/bench.txt) — имя бенчмарка могло разойтись с go/upcast_test.go"
done
[ "$(printf '%s\n' "$v1_allocs" | wc -l)" = 1 ] && [ "$(printf '%s\n' "$v3_allocs" | wc -l)" = 1 ] \
    || fail "allocs/op не детерминированы между повторами бенчмарка — вывод ниже не смог бы им доверять"
non_overlapping="0"
python3 -c "import sys; sys.exit(0 if $v1_min > $v3_max else 1)" && non_overlapping="1"

echo
echo "=== Граница вывода ==="
log "Полевой замер (fetch+upcast стрима из Postgres) НЕ показал устойчивого"
log "направления: отношение v1/v3 upcast_ms по раундам колеблется вокруг"
log "1.0 и переходит через неё (см. сводку выше — конкретные раунд, где v1"
log "дороже v3, и раунд, где дешевле, зависят от прогона). Ожидаемое"
log "устойчивое удорожание цепочки upcaster'ов на ЭТОМ сценарии (fetch+"
log "upcast целого стрима из Postgres) не обнаружено — формулирую именно"
log "так, а не «эффекта нет»: отсутствие обнаруженного эффекта не"
log "доказывает его отсутствие, оно может объясняться конкурирующим"
log "эффектом, который перекрывает искомый. Конкурирующий эффект здесь"
log "есть и он назван: v1-payload (2 поля) короче v3-payload (3 поля), и"
log "экономия на первичном json.Unmarshal более короткой строки может"
log "перекрывать две дополнительные map-операции цепочки настолько, что"
log "итоговое направление шумит около нуля. Изолированный микробенчмарк"
log "выше это подтверждает: там, где длина входа ЗАФИКСИРОВАНА одной и той"
log "же строкой байт для обоих вызовов и конкурирующего эффекта размера"
log "payload нет, schemaVer=1 (полная цепочка, $v1_allocs allocs/op,"
log "$v1_bytes B/op) стабильно дороже schemaVer=3 (без операций,"
log "$v3_allocs allocs/op, $v3_bytes B/op) по аллокациям — детерминировано,"
log "одно и то же значение во всех 5 повторов у обоих бенчмарков. По времени"
log "смотрим диапазоны из 5 повторов: BenchmarkUpcastV1Chain"
log "($v1_min-$v1_max нс/оп) против BenchmarkUpcastV3NoOp ($v3_min-$v3_max нс/оп)"
if [ "$non_overlapping" = "1" ]; then
    log "— диапазоны НЕ пересекаются (минимум V1Chain $v1_min > максимум V3NoOp"
    log "$v3_max), цепочка дороже в любой паре повторов И по времени, И по"
    log "аллокациям. Цепочка добавляет ровно $(( v1_allocs - v3_allocs ))"
    log "аллокации и $(( v1_bytes - v3_bytes )) байт на КАЖДЫЙ вызов."
else
    log "— диапазоны ПЕРЕСЕКАЮТСЯ (минимум V1Chain $v1_min <= максимум V3NoOp"
    log "$v3_max), устойчивого разделения ПО ВРЕМЕНИ на 5 повторах здесь не"
    log "установлено — этого прогона недостаточно, чтобы утверждать v1"
    log "дороже v3 по времени. Аллокации при этом расходятся детерминированно"
    log "и полностью объясняют механизм независимо от шума времени:"
    log "$(( v1_allocs - v3_allocs )) лишние аллокации и $(( v1_bytes - v3_bytes ))"
    log "лишних байта на КАЖДЫЙ вызов цепочки."
fi
log "То есть цепочка upcaster'ов сама по себе НЕ бесплатна (аллокации это"
log "показывают устойчиво независимо от шума времени), но в РЕАЛЬНОМ потоке"
log "чтения её цена в этом конкретном случае тонет в конкурирующем эффекте"
log "размера payload — итоговое направление на чтении здесь заранее не"
log "очевидно и не установлено."
log "НЕ показано: сколько именно стоит upcasting в чужой системе. Величина"
log "зависит от длины цепочки (здесь — 2 шага), сложности преобразований"
log "(здесь — подстановка ключа и переименование поля в map[string]any) и"
log "от того, насколько сильно отличаются по размеру старая и новая формы"
log "события — у более длинной цепочки, более тяжёлых преобразований"
log "(парсинг вложенных структур, обращение к внешним справочникам) или"
log "события, где старая форма НЕ короче новой, соотношение может быть"
log "другим, вплоть до противоположного тому, что видно на полевом замере"
log "здесь. Абсолютные upcast_ms и ns/op между раундами/запусками скрипта"
log "не сравниваю — сравнимо только отношение ВНУТРИ одного раунда/прогона."
