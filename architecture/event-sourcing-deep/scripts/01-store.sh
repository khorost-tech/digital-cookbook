#!/usr/bin/env bash
#
# 01-store.sh — артефакт к статье «Event store вглубь».
#
# Три замера:
#   A. append с ожидаемой версией: Postgres, KurrentDB, Kafka (попытка);
#   B. точечное чтение одного агрегата — где Kafka показывает свою цену;
#   C. catch-up-подписка: догон истории и переход в реальное время.
#
# Порядок движков циклически сдвигается по раундам: ни один не должен
# систематически оказываться первым, то есть на холодных кешах.
#
# РАВНЫЙ СТАРТ — не премиса, а проверенный факт. Раньше (до фикс-раунда 2)
# все три движка ЗАЯВЛЯЛИСЬ стартующими с пустой истории, но это было
# гарантировано только для Postgres (TRUNCATE перед раундом 1) и только
# ПОКА скрипт не запускали дважды: у KurrentDB и Kafka сброса не было
# вовсе, потоки/топик назывались одинаково ("tank-N" / "es-events") на
# КАЖДОМ запуске скрипта, и история молча накапливалась между прогонами.
# Арифметика фикстур это вскрыла: при равномерном распределении на
# tank-0 должно приходиться ~ops/64, а KurrentDB и Kafka показывали
# избыток 23-33% — остаток от предыдущих прогонов.
#
# Исправление в две части:
#   1) Каждый запуск ЭТОГО скрипта получает собственный уникальный
#      RUN_ID и строит из него STREAM_PREFIX/TOPIC — второй запуск не
#      может столкнуться с историей первого, даже если Postgres не
#      сбросить вовсе (TRUNCATE ниже — гигиена, не единственная гарантия
#      равного старта, как было раньше).
#   2) `-mode assert-fresh` ПРОВЕРЯЕТ пустоту делом (падает, если история
#      не пуста), а не полагается только на уникальность имени — см.
#      go/main.go, runAssertFresh.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/01-store.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-01
mkdir -p "$OUT"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

# jget ФАЙЛ КЛЮЧ [КЛЮЧ...] — значение по пути вложенных ключей.
# Отсутствующий ключ роняет python с KeyError, а не печатает пустоту:
# молчаливая пустая строка ниже прошла бы в printf как «значение».
jget() {
    python3 -c 'import json,sys
v=json.load(open(sys.argv[1]))
for k in sys.argv[2:]: v=v[k]
print(v)' "$@"
}

WORKERS="${WORKERS:-16}"
DUR="${DUR:-30s}"
ROUNDS="${ROUNDS:-3}"
# Число независимых прогонов catch-up — см. раздел C ниже.
CATCHUP_ROUNDS="${CATCHUP_ROUNDS:-6}"

# RUN_ID — уникален на КАЖДЫЙ запуск скрипта (наносекундный таймстамп).
# STREAM_PREFIX/TOPIC строятся из него: A и B этого прогона делят одно и то
# же пространство имён (B читает streamPrefix+"0", наполненный в A), но не
# пересекаются с любым другим запуском скрипта, прошлым или будущим.
RUN_ID="$(date +%s%N)"
STREAM_PREFIX="tank-${RUN_ID}-"
TOPIC="es-events-${RUN_ID}"
log "run_id=$RUN_ID stream_prefix=$STREAM_PREFIX topic=$TOPIC"

# Postgres переиспользуется другими профилями стенда (02-design.sh,
# 03-projections.sh) — TRUNCATE здесь снимает накопление МУСОРА в таблице
# events между прогонами разных профилей (гигиена, экономит место и время
# точечных запросов). Для равного старта ЭТОГО профиля TRUNCATE не
# обязателен (уникальный STREAM_PREFIX уже это даёт), но и не вреден.
log "чистим накопленную историю Postgres от прошлых профилей (гигиена, не гарантия равного старта — гарантия ниже)…"
psql_c -c "TRUNCATE events, snapshots, balances, checkpoints;" >/dev/null

echo "=== Предусловие: равный старт (проверка, не предположение) ==="
"$OUT/loadgen" -mode assert-fresh -streams 64 -stream-prefix "$STREAM_PREFIX" -topic "$TOPIC"

run_engine() { # $1 = engine, $2 = round
    local eng="$1"
    local r="$2"
    local f="$OUT/$eng-r$r.json"
    "$OUT/loadgen" -mode "${eng}-append" -workers "$WORKERS" -streams 64 -duration "$DUR" \
        -stream-prefix "$STREAM_PREFIX" -topic "$TOPIC" > "$f"
    printf '    %-9s ops=%-7s конфликтов=%-6s p50=%-8s p95=%s\n' \
        "$eng" "$(jget "$f" ops)" "$(jget "$f" conflicts)" \
        "$(jget "$f" p50_ms)" "$(jget "$f" p95_ms)"
}

echo "=== A. Append с ожидаемой версией ==="
for r in $(seq 1 "$ROUNDS"); do
    echo "  --- раунд $r:"
    case $(( r % 3 )) in
        1) run_engine postgres "$r"; run_engine kurrent "$r"; run_engine kafka "$r" ;;
        2) run_engine kurrent "$r";  run_engine kafka "$r";   run_engine postgres "$r" ;;
        0) run_engine kafka "$r";    run_engine postgres "$r"; run_engine kurrent "$r" ;;
    esac
done

echo
echo "=== B. Точечное чтение одного агрегата ==="
"$OUT/loadgen" -mode point-read-compare -streams 64 -duration 20s \
    -stream-prefix "$STREAM_PREFIX" -topic "$TOPIC" > "$OUT/point-read.json"
cat "$OUT/point-read.json"
log "у Kafka сравнивать надо не только время, но и число ПРОСМОТРЕННЫХ записей:"
log "именно оно объясняет, почему точечное чтение агрегата там дорого."
log "records_scanned = -1 у Postgres/KurrentDB значит «не измерено» (сервер"
log "отбирает событие на своей стороне, клиент не видит отброшенного) —"
log "это НЕ измеренное равенство просмотренного и подошедшего, см."
log "go/main.go, EngineTiming."

echo
echo "=== C. Catch-up-подписка: $CATCHUP_ROUNDS независимых прогонов ==="
# Величины здесь субмиллисекундные и миллисекундные — на такой шкале
# единичный замер не отличим от шума планировщика, поэтому прогонов
# несколько. Раньше скрипт делал ОДИН, а шесть прогонов, на которые
# ссылается статья, запускались руками; ревью справедливо указало на
# расхождение между тем, что делает скрипт, и тем, что заявлено. Теперь
# число прогонов — параметр скрипта, а не устная договорённость.
#
# Каждый прогон берёт СВОЙ уникальный стрим/топик (runID внутри
# runCatchupCompare), поэтому прогоны независимы и историю друг друга не
# видят: это не 6 повторов на растущей истории, а 6 стартов с нуля.
for c in $(seq 1 "$CATCHUP_ROUNDS"); do
    "$OUT/loadgen" -mode catchup-compare -duration 30s > "$OUT/catchup-$c.json"
    printf '  прогон %d: догон pg=%-10s ku=%-10s ka=%-10s | живая pg=%-10s ku=%-10s ka=%s\n' \
        "$c" \
        "$(jget "$OUT/catchup-$c.json" postgres catchup_ms)" \
        "$(jget "$OUT/catchup-$c.json" kurrent catchup_ms)" \
        "$(jget "$OUT/catchup-$c.json" kafka catchup_ms)" \
        "$(jget "$OUT/catchup-$c.json" postgres live_detect_ms)" \
        "$(jget "$OUT/catchup-$c.json" kurrent live_detect_ms)" \
        "$(jget "$OUT/catchup-$c.json" kafka live_detect_ms)"
done

echo
echo "=== Постусловие ==="
# Часть 09 (профиль 01 был единственным без единого проверяемого ассерта —
# "Постусловие" только печатало текст границы вывода). Ниже — проверки,
# которые МОГУТ провалиться:
#   1) равный старт подтверждён assert-fresh ВЫШЕ, до раунда 1 (падение там
#      остановило бы скрипт раньше этой точки — если мы здесь, проверка
#      прошла; печатаем это явно, а не молчим об уже пройденном шаге);
#   2) у Postgres и KurrentDB конфликты версий обязаны быть > 0 хотя бы в
#      одном раунде — модель со случайным выбором из 64 стримов и
#      несколькими воркерами на стрим ДОЛЖНА порождать конкуренцию; ноль
#      конфликтов во ВСЕХ раундах значит, что нагрузка не столкнула
#      воркеров, и весь раздел A о конкуренции за версию не о чем;
#   3) у Kafka конфликты обязаны быть РОВНО 0 во всех раундах — это не
#      «повезло», а прямое следствие отсутствия параметра expectedVersion в
#      producer API (см. AppendKafka); если conflicts != 0, что-то в коде
#      сломалось и Kafka завела условную запись, которой там быть не должно.
log "равный старт подтверждён ДО раунда 1 (assert-fresh выше упал бы, если бы история была не пуста)"

pg_conflicts_total=0
kurrent_conflicts_total=0
for r in $(seq 1 "$ROUNDS"); do
    pg_c="$(jget "$OUT/postgres-r$r.json" conflicts)"
    ku_c="$(jget "$OUT/kurrent-r$r.json" conflicts)"
    ka_c="$(jget "$OUT/kafka-r$r.json" conflicts)"
    pg_conflicts_total=$(( pg_conflicts_total + pg_c ))
    kurrent_conflicts_total=$(( kurrent_conflicts_total + ku_c ))
    [ "$ka_c" = "0" ] \
        || fail "раунд $r: у Kafka conflicts=$ka_c, ожидался 0 — producer API не должен давать условную запись; проверь AppendKafka"
done
[ "$pg_conflicts_total" -gt 0 ] \
    || fail "у Postgres conflicts=0 во ВСЕХ $ROUNDS раундах — конкуренция за версию не состоялась, замер A о ней несостоятелен"
[ "$kurrent_conflicts_total" -gt 0 ] \
    || fail "у KurrentDB conflicts=0 во ВСЕХ $ROUNDS раундах — конкуренция за версию не состоялась, замер A о ней несостоятелен"
log "конфликты состоялись: postgres=$pg_conflicts_total, kurrent=$kurrent_conflicts_total (сумма по $ROUNDS раундам), kafka=0 во всех раундах (ожидаемо — отсутствующая операция)"

#   4) каждый catch-up-прогон обязан ДОЧИТАТЬ всю предзаполненную историю:
#      catchup_events == prefill_events у всех трёх движков. Без этой
#      проверки «догон за 2.4 мс» мог бы означать не быстрый догон, а
#      подписку, которая прочитала часть истории и объявила себя догнавшей —
#      число миллисекунд выглядело бы тем убедительнее, чем меньше событий
#      реально прочитано.
for c in $(seq 1 "$CATCHUP_ROUNDS"); do
    for eng in postgres kurrent kafka; do
        want="$(jget "$OUT/catchup-$c.json" "$eng" prefill_events)"
        got="$(jget "$OUT/catchup-$c.json" "$eng" catchup_events)"
        [ "$want" = "$got" ] \
            || fail "catch-up прогон $c, $eng: догон прочитал $got событий из $want предзаполненных — замер догона несостоятелен"
    done
done
log "catch-up: во всех $CATCHUP_ROUNDS прогонах все три движка дочитали историю целиком (catchup_events = prefill_events)"

echo
log "ГРАНИЦА ВЫВОДА. Сравниваются три РАЗНЫХ продукта с разными гарантиями,"
log "а не «кто быстрее». Абсолютные величины к переносу непригодны;"
log "переносимы соотношения и качественные отличия — что движок умеет"
log "и чего не умеет. Kafka здесь не проигрывает соревнование, а показывает,"
log "что у неё отсутствует операция, на которой держится весь паттерн."
