#!/usr/bin/env bash
#
# 06-rebuild-bluegreen.sh — дозамер к артефакту 03 («Проекции вглубь»):
# статья 3 серии честно вынесла за охват тему пересборки проекции без
# остановки чтения (blue-green rebuild). Этот скрипт закрывает её отдельным
# замером.
#
# Сценарий: на уже накопленной истории событий (та же форма наполнения, что
# в 03-projections.sh — append-fine, 64 стрима) собирается ВТОРАЯ проекция
# (`balances_shadow`) с global_pos=0 заново, ПОКА боевой online-проектор
# (`balances`) продолжает применять события, и ПОКА независимый писатель
# продолжает дописывать новые события (хвост, который shadow обязана
# догнать до подмены). По готовности — атомарная подмена: тройной ALTER
# TABLE ... RENAME в одной транзакции Postgres (см. go/rebuild.go,
# swapBlueGreen), вместе с переносом чекпоинта online-проектора на позицию
# shadow (иначе online после подмены применил бы уже применённые shadow
# события повторно — см. комментарий в go/rebuild.go).
#
# Проверяются четыре вещи, каждая — реальный ассерт, способный провалиться:
#   1. shadow-проекция ПЕРЕД подменой совпадает со сверткой events
#      (FULL OUTER JOIN, не INNER — см. артефакт 03, «Фикс-раунд 1»).
#   2. боевая проекция ПОСЛЕ подмены (когда online догнал то немногое, что
#      могло появиться уже после переноса чекпоинта) тоже совпадает со
#      сверткой events.
#   3. online-проектор РЕАЛЬНО продвинулся (> 0 событий) за время, пока шла
#      shadow-пересборка — иначе "без остановки" нечем подтвердить: online
#      мог бы уже закончить работу ДО старта пересборки.
#   4. проба чтения боевой таблицы (раз в несколько мс на протяжении всего
#      окна пересборки и подмены) не поймала НИ ОДНОЙ ошибки.
#
# Не меньше трёх прогонов подряд, TRUNCATE ... RESTART IDENTITY перед
# КАЖДЫМ — иначе global_pos (BIGSERIAL) продолжится с прошлого прогона и
# позиция станет несопоставима со счётчиком проигранных строк (та же
# ловушка, что уже ловилась в артефакте 03, см. FIXTURES.md).
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/06-rebuild-bluegreen.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-06
mkdir -p "$OUT"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

jget() { # $1 = файл, $2 = ключ
    python3 -c "import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])" "$1" "$2"
}

# FILL_DURATION/FILL_WORKERS/FILL_STREAMS — тот же объём, что в артефакте 03
# (scripts/03-projections.sh: append-fine, 16 воркеров, 64 стрима, 20с) —
# задача явно просит объём "порядка того, что уже используется в
# артефакте 03", не отдельно подобранное число.
FILL_DURATION="${FILL_DURATION:-20s}"
FILL_WORKERS="${FILL_WORKERS:-16}"
FILL_STREAMS="${FILL_STREAMS:-64}"
# WRITE_DURATION — окно, на которое запускается независимый писатель ВНУТРИ
# rebuild-bluegreen (см. go/rebuild.go, флаг -duration режима): он и
# производит хвост новых событий, который shadow обязана догнать до
# подмены. Короче FILL_DURATION — иначе прогон растягивается без
# необходимости; достаточно, чтобы shadow гарантированно не успела
# завершить дренаж раньше, чем писатель произведёт хоть что-то (проверено
# постфактум числом writer_events_appended > 0 в каждом прогоне ниже).
WRITE_DURATION="${WRITE_DURATION:-3s}"
ROUNDS="${ROUNDS:-3}"

declare -a rebuild_ms_list shadow_events_list online_progress_list probe_fail_list writer_events_list history_list

for i in $(seq 1 "$ROUNDS"); do
    echo
    echo "=== Прогон $i/$ROUNDS ==="
    # RESTART IDENTITY обязателен: global_pos — BIGSERIAL, обычный TRUNCATE
    # сиквенс не трогает (та же ловушка, что в артефакте 03 — см.
    # FIXTURES.md, «дырки»/сиквенс, и в артефакте 04).
    psql_c -c "TRUNCATE events, balances, balances_shadow, checkpoints RESTART IDENTITY;" >/dev/null

    echo "--- Наполняем историю (append-fine, ${FILL_WORKERS}w/${FILL_STREAMS}s/${FILL_DURATION}) ---"
    "$OUT/loadgen" -mode append-fine -workers "$FILL_WORKERS" -streams "$FILL_STREAMS" -duration "$FILL_DURATION" \
        > "$OUT/fill-$i.json"
    history="$(sql_value "SELECT count(*) FROM events;")"
    log "прогон $i: событий в сторе перед пересборкой: $history"

    echo "--- Blue-green пересборка (shadow + online + писатель + проба чтения) ---"
    "$OUT/loadgen" -mode rebuild-bluegreen -workers "$FILL_WORKERS" -streams "$FILL_STREAMS" \
        -duration "$WRITE_DURATION" > "$OUT/rebuild-$i.json"
    cat "$OUT/rebuild-$i.json"

    rebuild_ms="$(jget "$OUT/rebuild-$i.json" rebuild_ms)"
    shadow_events="$(jget "$OUT/rebuild-$i.json" shadow_events_replayed)"
    online_progress="$(jget "$OUT/rebuild-$i.json" online_progress_events)"
    probe_checks="$(jget "$OUT/rebuild-$i.json" probe_checks)"
    probe_failures="$(jget "$OUT/rebuild-$i.json" probe_failures)"
    writer_events="$(jget "$OUT/rebuild-$i.json" writer_events_appended)"
    diff_shadow="$(jget "$OUT/rebuild-$i.json" reconcile_shadow_vs_fold_diff)"
    diff_post_swap="$(jget "$OUT/rebuild-$i.json" reconcile_post_swap_vs_fold_diff)"

    echo
    echo "--- Ассерты прогона $i ---"

    [ "$diff_shadow" = "0" ] \
        || fail "прогон $i: shadow-проекция разошлась со сверткой на $diff_shadow стримах ДО подмены"
    log "прогон $i: ассерт 1 (shadow == свёртка до подмены) — пройден (0 расхождений)"

    [ "$diff_post_swap" = "0" ] \
        || fail "прогон $i: боевая проекция разошлась со сверткой на $diff_post_swap стримах ПОСЛЕ подмены"
    log "прогон $i: ассерт 2 (боевая проекция == свёртка после подмены) — пройден (0 расхождений)"

    [ "$online_progress" -gt 0 ] \
        || fail "прогон $i: online-проектор продвинулся на $online_progress событий за время пересборки — "\
"должно быть > 0, иначе 'без остановки' нечем подтвердить (online мог уже закончить ДО старта shadow)"
    log "прогон $i: ассерт 3 (online продвинулся во время пересборки) — пройден ($online_progress событий)"

    [ "$probe_checks" -gt 0 ] \
        || fail "прогон $i: проба чтения не сделала ни одной попытки — окно пересборки замер не захватил"
    [ "$probe_failures" = "0" ] \
        || fail "прогон $i: проба чтения поймала $probe_failures ошибок из $probe_checks попыток во время пересборки/подмены"
    log "прогон $i: ассерт 4 (чтение не прерывалось) — пройден (0 ошибок из $probe_checks попыток)"

    [ "$writer_events" -gt 0 ] \
        || fail "прогон $i: писатель не записал ни одного события за WRITE_DURATION=$WRITE_DURATION — "\
"хвоста для догона не было, замер 'пересборка без остановки записи' бессмыслен"
    log "прогон $i: писатель произвёл хвост в $writer_events событий за время пересборки"

    rebuild_ms_list+=("$rebuild_ms")
    shadow_events_list+=("$shadow_events")
    online_progress_list+=("$online_progress")
    probe_fail_list+=("$probe_failures")
    writer_events_list+=("$writer_events")
    history_list+=("$history")
done

echo
echo "=== Сводка по $ROUNDS прогонам ==="
printf '%-8s %-12s %-14s %-20s %-16s %-16s\n' "прогон" "история" "rebuild_ms" "shadow_events" "online_progress" "writer_events"
for i in $(seq 1 "$ROUNDS"); do
    idx=$((i - 1))
    printf '%-8s %-12s %-14s %-20s %-16s %-16s\n' "$i" "${history_list[$idx]}" "${rebuild_ms_list[$idx]}" \
        "${shadow_events_list[$idx]}" "${online_progress_list[$idx]}" "${writer_events_list[$idx]}"
done

echo
log "ГРАНИЦА ВЫВОДА. Время пересборки зависит от объёма истории и от железа"
log "конкретного стенда — переносим не абсолютное число, а метод (shadow +"
log "живой online-проектор + независимый писатель + проба чтения + атомарный"
log "RENAME с переносом чекпоинта) и порядок величины на ЭТОМ объёме истории."
log "НЕ проверялось: поведение при отказе (падении процесса) на середине"
log "пересборки; конкуренция нескольких одновременных пересборок; поведение"
log "под конкурентной ЗАПИСЬЮ именно в момент самого RENAME (проба здесь —"
log "только читатель; писатель работает ДО момента подмены, а не во время"
log "самой транзакции RENAME, потому что WRITE_DURATION истекает раньше, чем"
log "начинается сама транзакция подмены — см. go/rebuild.go, writerDone)."
