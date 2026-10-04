#!/usr/bin/env bash
# Артефакт 4: partition key в Scylla под дефолтным Murmur3Partitioner —
# проверка МИФА «монотонный (растущий счётчик) ключ = горячая партиция»,
# перенесённого по аналогии с order-preserving хранилищами (B-tree). Под
# хеш-партиционером миф неверен: Murmur3 хеширует значение partition key
# независимо от его происхождения, поэтому монотонный bigint покрывает
# token-кольцо ТАК ЖЕ ШИРОКО, как случайный uuid. Артефакт измеряет именно
# ШИРИНУ ПОКРЫТИЯ — span = max(token(pk)) - min(token(pk)), т.е. диапазон
# занятых токенов, а НЕ гистограмму распределения внутри этого диапазона:
# равномерность значений ВНУТРИ диапазона — свойство самой хеш-функции
# Murmur3, а не то, что здесь напрямую замерено. Один хост: показываем
# структурный факт про раскладку токенов, не нагрузку реального кластера.
#
# Версионные особенности Scylla 2026.2.1, проверено живьём (НЕ из документации):
#   1. keyspace с replication={'class':'SimpleStrategy',...} не создаётся —
#      "ConfigurationException: SimpleStrategy doesn't support tablet
#      replication" (в этой версии таблеты/tablets включены по умолчанию).
#      Используем короткую форму NetworkTopologyStrategy с replication_factor
#      без явного имени датацентра — Scylla раскладывает сама.
#   2. cqlsh -e "<...текст с завершающим \n перед закрывающей кавычкой>"
#      возвращает rc=2 (SyntaxException: no viable alternative at input ';')
#      на пустом хвостовом операторе — даже если все реальные CQL-запросы
#      выполнились успешно. Под set -euo pipefail это ломает скрипт на
#      ровном месте. Чинится через $(...): командная подстановка сама
#      обрезает завершающий перевод строки.
#   3. select max(token(pk)) - min(token(pk)) — SyntaxException: арифметика
#      над результатами агрегатных функций в одном select не поддерживается.
#      Берём max(token(pk)) и min(token(pk)) как две колонки одного запроса,
#      разницу считаем на стороне клиента (python).
#
# Честный результат измерения (не подгонка): за 6 прогонов span(random)
# оказался шире span(monotonic) лишь 1 раз из 6 — разница около нуля, шум,
# а не системный эффект. Артефакт оставлен как ЧЕСТНЫЙ НЕГАТИВ: миф
# «монотонный ключ = горячая партиция под Murmur3» опровергнут. Реальная
# горячая партиция в Scylla возникает не от уникального монотонного ключа,
# а от ПОВТОРНОГО использования одного и того же (или немногих) значений
# partition key — принципиально другой механизм, чем физическая локальность
# B-tree в PostgreSQL. Реальный обратный трейдоф (ranged shard key
# концентрирует запись) показан отдельно на Mongo.
#
# Проверка ниже — честный позитив: сигнал засчитывается, если ОБА span
# (random и monotonic) покрывают ≥ 0.9 полного диапазона int64 — т.е. оба
# широко покрывают token-кольцо, монотонность НЕ концентрирует. Если хотя
# бы один span уже — DEMO FAILED, rc=1, как и требует фальсифицируемость.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
# На Windows-хосте (Git Bash) системный python по умолчанию пишет stdout в
# кодировке консоли (cp1251), а не UTF-8 — кириллица в print() превращается
# в мусор при перенаправлении вывода. На Linux/CI это не проявляется, но
# фиксируем явно для воспроизводимости в обеих средах.
export PYTHONIOENCODING=utf-8
# Интерпретатор Python: в WSL/многих дистрибутивах есть только python3, на
# Windows Git Bash — только python. Берём python, затем fallback на python3
# (порядок неслучаен: на Windows `python3` часто указывает на нерабочую
# Store-заглушку, а рабочий бинарь называется python).
PY=$(command -v python || command -v python3 || true)
if [ -z "$PY" ]; then
  echo "DEMO FAILED: не найден интерпретатор Python (нужен python или python3)" >&2
  exit 1
fi
N=${N:-20000}
CQL=(docker compose -f compose/compose.yml exec -T scylla cqlsh)

echo "[demo] проверка связи со Scylla..." >&2
if ! "${CQL[@]}" -e "select now() from system.local;" >/dev/null 2>&1; then
  echo "DEMO FAILED: Scylla недоступна (compose-scylla-1, localhost:9043 на хосте)" >&2
  exit 1
fi

DDL=$(cat <<'EOF'
create keyspace if not exists ids with replication={'class':'NetworkTopologyStrategy','replication_factor':1};
drop table if exists ids.rnd; drop table if exists ids.mono;
create table ids.rnd (pk uuid primary key, payload text);
create table ids.mono (pk bigint primary key, payload text);
EOF
)
"${CQL[@]}" -e "$DDL"

# Наполнение через простой цикл CQL (маленькое N: цель — распределение, не throughput).
"$PY" - "$N" <<'PY' | "${CQL[@]}"
import sys,uuid
n=int(sys.argv[1]); p="x"*32
print("use ids;")
for i in range(n):
    print(f"insert into rnd(pk,payload) values ({uuid.uuid4()},'{p}');")
    print(f"insert into mono(pk,payload) values ({i},'{p}');")
PY

# Разброс токенов: max(token(pk)) и min(token(pk)) отдельными колонками
# (арифметика над агрегатами в select не поддержана — грабли #3 выше),
# разницу считаем в python. Данные — 4-я строка вывода cqlsh (пустая
# строка, заголовок, разделитель, значения).
span() {
  local q
  q=$(cat <<EOF
select max(token(pk)) as mx, min(token(pk)) as mn from ids.$1;
EOF
)
  "${CQL[@]}" -e "$q" | sed -n '4p'
}
read -r mx_r _ mn_r <<<"$(span rnd)"
read -r mx_m _ mn_m <<<"$(span mono)"

read -r sr sm <<<"$("$PY" - "$mx_r" "$mn_r" "$mx_m" "$mn_m" <<'PY'
import sys
mx_r, mn_r, mx_m, mn_m = (int(x) for x in sys.argv[1:5])
print(mx_r - mn_r, mx_m - mn_m)
PY
)"
echo "token span: random=$sr monotonic=$sm"

# Честный позитив: сигнал засчитывается, если ОБА span покрывают почти весь
# диапазон int64 (Murmur3 хеширует, монотонность не концентрирует). Разницу
# random/monotonic печатаем отдельно как долю от диапазона — это шум, не эффект.
"$PY" - "$sr" "$sm" <<'PY'
import sys
sr, sm = abs(int(sys.argv[1])), abs(int(sys.argv[2]))
FULL = 18446744073709551615
thr = 0.9 * FULL
diff_pct = 100.0 * abs(sr - sm) / FULL
if sr >= thr and sm >= thr:
    print("SIGNAL: под Murmur3 оба ключа покрывают ~весь token-диапазон — монотонность НЕ концентрирует (разница random/monotonic %.4f%% = шум)" % diff_pct)
    sys.exit(0)
print("DEMO FAILED: не оба широки (r=%d m=%d thr=%d)" % (sr, sm, thr)); sys.exit(1)
PY
