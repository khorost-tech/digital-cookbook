#!/usr/bin/env bash
#
# 03-projections.sh — артефакт к статье «Проекции вглубь».
#
# Ключевая проверка: проектор убивается на середине, после рестарта проекция
# ОБЯЗАНА совпасть со свёрткой событий. Расхождение = замер провален,
# а не «небольшая погрешность».
#
# Запуск (из architecture/event-sourcing-deep/):  bash scripts/03-projections.sh

set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"
# shellcheck source=lib.sh
source scripts/lib.sh

OUT=/tmp/esd-03
mkdir -p "$OUT"
( cd go && go build -o "$OUT/loadgen" . ) || fail "loadgen не собрался"

# RESTART IDENTITY обязателен здесь: global_pos — BIGSERIAL, обычный TRUNCATE
# сиквенс не трогает. Задачи 1-2 и предыдущие прогоны этого же скрипта уже
# толкали сиквенс вперёд (застигнуто фактом: last_value 159268 при пустой
# events) — без RESTART IDENTITY чекпоинт после обрыва сравнивался бы с
# ЧИСЛОМ строк (total = count(*)), а хранит АБСОЛЮТНУЮ позицию в сиквенсе
# (global_pos), и эти две величины из разных шкал: чекпоинт мог оказаться
# БОЛЬШЕ total, хотя реально обработана лишь часть истории. Первый прогон
# без RESTART IDENTITY так и упал: чекпоинт 128552 при total 31584.
psql_c -c "TRUNCATE events, balances, checkpoints RESTART IDENTITY;" >/dev/null

echo "=== Наполняем историю ==="
"$OUT/loadgen" -mode append-fine -workers 16 -streams 64 -duration 20s > "$OUT/fill.json"
total="$(sql_value "SELECT count(*) FROM events;")"
log "событий в сторе: $total"

echo
echo "=== Проектор с чекпоинтом, убийство на середине ==="
"$OUT/loadgen" -mode project -duration 3s > "$OUT/proj-1.json" || true
mid="$(sql_value "SELECT xid::text || ' / ' || global_pos FROM checkpoints WHERE projector = 'balances';")"
ops1="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ops"])' "$OUT/proj-1.json")"
log "чекпоинт после обрыва: $mid; обработано за 3с: $ops1 из $total"

# Обрыв обязан прийтись на СЕРЕДИНУ: не до первой пачки и не после
# последней. Иначе проверка бессмысленна — рестарт с нуля и рестарт без
# необработанных событий тривиально проходят без потерь и дублей.
#
# Сравнивается число ОБРАБОТАННЫХ событий с общим числом событий, а НЕ
# чекпоинт с count(*): ревью указало, что это величины из разных шкал.
# global_pos — позиция в сиквенсе, где есть дырки от откаченных
# конфликтующих транзакций, а count(*) — число строк; их сравнение давало
# верный ответ только по счастливому совпадению масштабов. Плюс с
# фикс-раунда 4 чекпоинт вообще стал парой (xid, global_pos), и одно его
# число сравнивать с чем-либо тем более нельзя.
[ "$ops1" -gt 0 ] && [ "$ops1" -lt "$total" ] \
    || fail "обрыв не пришёлся на середину (обработано $ops1 из $total) — проверка бессмысленна"

"$OUT/loadgen" -mode project -duration 60s > "$OUT/proj-2.json"

# Сумма обработанных событий двух прогонов обязана совпасть с числом событий
# в сторе. Это независимая от SQL-сверки ниже проверка: та сравнивает
# СОСТОЯНИЕ (суммы литров), эта — СЧЁТ обработанных событий. Потеря и
# двойная обработка видны здесь как арифметика, а не как расхождение
# балансов, которое могло бы взаимно скомпенсироваться.
ops1="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ops"])' "$OUT/proj-1.json")"
ops2="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ops"])' "$OUT/proj-2.json")"
log "обработано событий: прогон 1 (до обрыва) $ops1 + прогон 2 (после рестарта) $ops2 = $(( ops1 + ops2 )), событий в сторе $total"
[ "$(( ops1 + ops2 ))" = "$total" ] \
    || fail "обработано $(( ops1 + ops2 )) событий при $total в сторе — потеря или двойная обработка на уровне счёта"

echo
echo "=== Сверка: список допустимых типов событий ==="
# Проверка ДРУГОГО рода, не арифметическая, и она обязана идти ПЕРЕД сверкой
# сумм. Сверка сумм слепа к неизвестному типу события: её CASE ... ELSE 0
# сворачивает незнакомое событие в ноль ровно так же, как это делал проектор
# до фикса, — обе стороны приходят к одному неверному значению и рапортуют
# 0 расхождений. Ревью показало это контрпримером OxygenLeaked{liters:99}:
# ops=1, errors=0, liters=0, diff_rows=0. Сумма не ловит того, чего обе её
# стороны одинаково не умеют считать.
unknown="$(sql_value "
  SELECT coalesce(string_agg(event_type || ' (' || n || ' шт.)', ', '), '')
  FROM (SELECT event_type, count(*) AS n FROM events
        WHERE event_type NOT IN ('TankRegistered','OxygenAdded','OxygenConsumed')
        GROUP BY event_type) u;")"
[ -z "$unknown" ] \
    || fail "в events есть события неизвестных типов: $unknown — ни проектор, ни сверка не умеют их сворачивать"
log "неизвестных типов событий нет (известные: TankRegistered, OxygenAdded, OxygenConsumed)"

echo
echo "=== Сверка: проекция против свёртки событий ==="
# FULL OUTER JOIN, а не INNER: INNER молча выбросил бы из результата стрим,
# потерянный проекцией целиком (он есть в f из events, но отсутствует в b),
# и diff_rows остался бы нулём при потерянном стриме. Расхождением считаем
# ТРИ случая: числа разошлись; стрим есть в событиях, но отсутствует в
# проекции (f.stream_id заполнен, b.stream_id NULL); стрим есть в проекции,
# но отсутствует в событиях (наоборот).
diff_rows="$(sql_value "
  SELECT count(*) FROM (
    SELECT e.stream_id,
           sum(CASE WHEN e.event_type = 'OxygenAdded' THEN (e.data->>'liters')::numeric
                    WHEN e.event_type = 'OxygenConsumed' THEN -(e.data->>'liters')::numeric
                    ELSE 0 END) AS folded
    FROM events e GROUP BY e.stream_id
  ) f FULL OUTER JOIN balances b USING (stream_id)
  WHERE f.stream_id IS NULL
     OR b.stream_id IS NULL
     OR abs(f.folded - b.liters) > 0.0001;")"
[ "$diff_rows" = "0" ] \
    || fail "проекция разошлась со свёрткой на $diff_rows стримах — потери, дубли или пропавший стрим целиком"
log "проекция совпала со свёрткой на всех стримах после обрыва и рестарта"

echo
echo "=== Постусловие ==="
log "ГРАНИЦА ВЫВОДА. Показано, что применение с чекпоинтом в одной транзакции"
log "переживает обрыв без потерь и дублей на ЭТОМ объёме истории и при ЭТОМ"
log "способе обрыва. Отказ посреди коммита или потеря соединения к базе"
log "здесь не воспроизводились."
