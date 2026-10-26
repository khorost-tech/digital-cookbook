#!/usr/bin/env bash
#
# 07-projector-safety.sh — воспроизводимые контрпримеры к проектору.
#
# Эти сюжеты раньше жили только стенограммой в FIXTURES.md: их можно было
# прочитать, но не перезапустить. После фикс-раунда 5 они — исполняемый
# сценарий, который падает, если защита сломается. Именно этого требует
# правило серии: проверка, которую никогда не видели падающей, ничего не
# гарантирует, а проверка, которую нельзя повторить, ничего не гарантирует
# завтра.
#
# Четыре сюжета, каждый с контролем:
#
#   A. BIGSERIAL не является порядком коммитов. Задержанная транзакция
#      получает МЕНЬШУЮ позицию и коммитится ПОЗЖЕ. Показано, что курсор по
#      одному global_pos потерял бы её событие навсегда, а нынешний курсор
#      (xid, global_pos) с водяным знаком — не теряет.
#   B. Первым событием нового стрима приходит версия 20 — проектор обязан
#      отказаться, а не создать строку сразу на двадцатой версии.
#   C. Событие неизвестного типа — проектор обязан отказаться, и сверка
#      обязана отдельно поймать его списком допустимых типов (сумма его не
#      ловит: обе стороны сворачивают незнакомое событие в один и тот же ноль).
#   D. Противоречие между event_type и data->>'type' — ограничение схемы не
#      даёт его записать, а на снятом ограничении (данные старой схемы)
#      проектор обязан отказаться применять такое событие.
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/07-projector-safety.sh

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-07
mkdir -p "$OUT"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

reset() { psql_c -c "TRUNCATE events, balances, checkpoints RESTART IDENTITY;" >/dev/null; }
insert_event() { # $1=стрим $2=версия $3=тип $4=литры
    psql_c -c "INSERT INTO events (stream_id, version, event_type, data)
               VALUES ('$1', $2, '$3', '{\"type\":\"$3\",\"liters\":$4}');" >/dev/null
}

fails=0
expect_fail() { # $1=подпись, далее команда
    local label="$1"; shift
    if "$@" >"$OUT/last.log" 2>&1; then
        echo "  ПРОВАЛ: $label — команда завершилась успешно, ожидался отказ"
        fails=$((fails + 1))
    else
        echo "  ok: $label — отказ, как и требуется:"
        grep -iE "разрыв|неизвестн|противоречив|ОШИБКА" "$OUT/last.log" | head -2 | sed 's/^/       /'
    fi
}
expect_ok() { # $1=подпись, далее команда
    local label="$1"; shift
    if "$@" >"$OUT/last.log" 2>&1; then
        echo "  ok: $label — прошло, как и требуется"
    else
        echo "  ПРОВАЛ: $label — ожидался успех, получен отказ:"
        tail -2 "$OUT/last.log" | sed 's/^/       /'
        fails=$((fails + 1))
    fi
}

echo "=== A. BIGSERIAL не является порядком коммитов ==="
reset
# Транзакция A: пишет (берёт global_pos=1) и висит, не коммитясь.
docker exec -i "$PG_CONTAINER" psql -U esdemo -d esdemo -q >/dev/null 2>&1 <<'SQL' &
BEGIN;
INSERT INTO events (stream_id, version, event_type, data)
VALUES ('slow', 1, 'OxygenAdded', '{"type":"OxygenAdded","liters":5}');
SELECT pg_sleep(10);
COMMIT;
SQL
A_PID=$!
sleep 2
# Транзакция B: пишет (берёт global_pos=2) и коммитится сразу.
insert_event fast 1 OxygenAdded 7

visible_max="$(sql_value 'SELECT coalesce(max(global_pos), 0) FROM events;')"
log "пока A висит, видима только запись B; max(global_pos) = $visible_max"
log "курсор ПО ОДНОМУ global_pos поставил бы чекпоинт на $visible_max"

"$OUT/loadgen" -mode project -duration 4s >"$OUT/a-1.json" 2>&1
log "нынешний проектор за это время применил: $(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ops"])' "$OUT/a-1.json") событий (ожидается 0 — ждём A)"

wait $A_PID
# Что БЫЛО БЫ со старым курсором: события с позицией <= visible_max больше
# никогда не попадут под условие global_pos > checkpoint.
lost="$(sql_value "SELECT count(*) FROM events WHERE global_pos <= $visible_max AND stream_id = 'slow';")"
[ "$lost" = "1" ] \
    || fail "сценарий не воспроизвёлся: у задержанной транзакции позиция не оказалась ниже чекпоинта"
log "после коммита A её событие имеет позицию <= $visible_max — старый курсор не выбрал бы его НИКОГДА"

"$OUT/loadgen" -mode project -duration 4s >"$OUT/a-2.json" 2>&1
applied="$(sql_value 'SELECT count(*) FROM balances;')"
if [ "$applied" = "2" ]; then
    echo "  ok: нынешний курсор (xid, global_pos) применил оба события ($applied из 2)"
else
    echo "  ПРОВАЛ: в проекции $applied стрим(ов) из 2 — событие потеряно"
    fails=$((fails + 1))
fi

echo
echo "=== B. Первое событие нового стрима — версия 20 ==="
reset
insert_event gap20 20 OxygenAdded 7
expect_fail "разрыв версий на новом стриме" "$OUT/loadgen" -mode project -duration 5s
rows="$(sql_value 'SELECT count(*) FROM balances;')"
[ "$rows" = "0" ] || { echo "  ПРОВАЛ: строка создана несмотря на разрыв"; fails=$((fails + 1)); }

reset
insert_event ok1 1 OxygenAdded 7
expect_ok "контроль: версия 1 на новом стриме применяется" "$OUT/loadgen" -mode project -duration 5s

echo
echo "=== C. Событие неизвестного типа ==="
reset
insert_event leak 1 OxygenLeaked 99
expect_fail "проектор на неизвестном типе" "$OUT/loadgen" -mode project -duration 5s
rows="$(sql_value 'SELECT count(*) FROM balances;')"
[ "$rows" = "0" ] || { echo "  ПРОВАЛ: строка создана по неизвестному типу"; fails=$((fails + 1)); }

# И отдельно — сверка.
#
# Здесь важно воспроизвести состояние ЧЕСТНО. Нынешний проектор отказался,
# строки в balances нет, и сверка по сумме поймала бы это как «стрим есть в
# событиях, но отсутствует в проекции» — через FULL OUTER JOIN, а вовсе не
# по своей арифметике. Слепота суммы проявляется на ДРУГОМ состоянии: том,
# которое создавал проектор ДО фикса — строка есть, версия повышена, остаток
# ноль. Воспроизводим это состояние руками и смотрим на обе проверки.
psql_c -c "INSERT INTO balances (stream_id, version, liters) VALUES ('leak', 1, 0);" >/dev/null
log "воспроизведено состояние, которое создавал прежний проектор: balances(leak, version=1, liters=0)"
sum_diff="$(sql_value "
  SELECT count(*) FROM (
    SELECT e.stream_id,
           sum(CASE WHEN e.event_type = 'OxygenAdded' THEN (e.data->>'liters')::numeric
                    WHEN e.event_type = 'OxygenConsumed' THEN -(e.data->>'liters')::numeric
                    ELSE 0 END) AS folded
    FROM events e GROUP BY e.stream_id
  ) f FULL OUTER JOIN balances b USING (stream_id)
  WHERE f.stream_id IS NULL OR b.stream_id IS NULL OR abs(f.folded - b.liters) > 0.0001;")"
if [ "$sum_diff" = "0" ]; then
    echo "  ok: сверка ПО СУММЕ даёт diff_rows=0 — она СЛЕПА к неизвестному типу"
    echo "       (обе стороны свернули незнакомое событие в один и тот же ноль)"
else
    echo "  ПРОВАЛ: ожидалось diff_rows=0 (слепота суммы), получено $sum_diff —"
    echo "       контрпример не воспроизводит состояние прежнего проектора"
    fails=$((fails + 1))
fi
unknown="$(sql_value "
  SELECT coalesce(string_agg(event_type || ' (' || n || ' шт.)', ', '), '')
  FROM (SELECT event_type, count(*) AS n FROM events
        WHERE event_type NOT IN ('TankRegistered','OxygenAdded','OxygenConsumed')
        GROUP BY event_type) u;")"
if [ -n "$unknown" ]; then
    echo "  ok: проверка списка типов поймала: $unknown"
else
    echo "  ПРОВАЛ: проверка списка типов не увидела неизвестный тип"
    fails=$((fails + 1))
fi

echo
echo "=== D. Противоречие между event_type и data->>'type' ==="
reset
# Рубеж 1 — ограничение схемы: противоречивая строка не должна записаться.
if psql_c -c "INSERT INTO events (stream_id, version, event_type, data)
              VALUES ('mismatch', 1, 'OxygenConsumed', '{\"type\":\"OxygenAdded\",\"liters\":7}');" \
              >/dev/null 2>&1; then
    echo "  ПРОВАЛ: противоречивая строка записалась — ограничение не работает"
    fails=$((fails + 1))
else
    echo "  ok: ограничение events_type_matches_payload не дало записать противоречие"
fi

# Рубеж 2 — проверка на чтении. Она защищает от строк, записанных ДО
# появления ограничения (append-only стор переживает свои схемы). Чтобы
# показать её работающей, ограничение временно снимается — ровно так
# выглядели бы данные, наполненные старой версией схемы.
psql_c -c "ALTER TABLE events DROP CONSTRAINT events_type_matches_payload;" >/dev/null
psql_c -c "INSERT INTO events (stream_id, version, event_type, data)
           VALUES ('mismatch', 1, 'OxygenConsumed', '{\"type\":\"OxygenAdded\",\"liters\":7}');" >/dev/null
log "ограничение временно снято, противоречивая строка записана (имитация данных старой схемы)"
expect_fail "проектор на противоречивом типе" "$OUT/loadgen" -mode project -duration 5s
rows="$(sql_value 'SELECT count(*) FROM balances;')"
[ "$rows" = "0" ] || { echo "  ПРОВАЛ: событие применено несмотря на противоречие"; fails=$((fails + 1)); }
psql_c -c "ALTER TABLE events ADD CONSTRAINT events_type_matches_payload
           CHECK (data ? 'type' AND data->>'type' = event_type);" >/dev/null 2>&1 \
    || { psql_c -c "TRUNCATE events, balances, checkpoints RESTART IDENTITY;" >/dev/null
         psql_c -c "ALTER TABLE events ADD CONSTRAINT events_type_matches_payload
                    CHECK (data ? 'type' AND data->>'type' = event_type);" >/dev/null; }
log "ограничение возвращено на место"

reset
echo
echo "=== Итог ==="
[ "$fails" = "0" ] || fail "не пройдено проверок: $fails"
log "все контрпримеры воспроизведены, все защиты сработали"
