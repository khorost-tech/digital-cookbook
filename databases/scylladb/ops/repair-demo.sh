#!/usr/bin/env bash
# repair-demo.sh — Стенд #3 серии "ScyllaDB: глубокое погружение", живая
# демонстрация repair: остановить один узел, писать при 2/3 (создаёт
# расхождение реплик), поднять узел, явный `nodetool cluster repair`,
# показать до/после на самом отставшем узле (CONSISTENCY ONE, без
# координации других реплик).
#
# ЧЕСТНАЯ НАХОДКА (проверено живьём): keyspace `telemetry` создан с tablets
# ('tablets = {enabled: true}' — дефолт для новых keyspace в этой версии
# ScyllaDB). Буквальная команда из брифа `nodetool repair -pr telemetry`
# НЕ подходит для tablet keyspace — падает с honest-ошибкой самого nodetool:
#   "nodetool repair repairs only vnode keyspaces! To repair tablet
#    keyspaces use nodetool cluster repair."
# Рабочая замена — `nodetool cluster repair --keyspace telemetry` (её и
# использует этот скрипт). У неё же, в отличие от старого vnode-`nodetool
# repair` (там "repairs on ScyllaDB are always full" — инкрементального
# режима нет вообще), ЕСТЬ `--incremental-mode disabled|incremental|full` —
# проверено живьём, оба нетривиальных значения принимаются без ошибки.
# Этот скрипт использует режим по умолчанию (полный обход всех таблиц
# keyspace, без --incremental-mode) — так честнее для демонстрационного
# прогона на idle-кластере без предварительного baseline repaired-состояния.
#
# Синхронный: каждый шаг печатает результат ДО следующего, никаких фоновых
# джобов. Кластер возвращается к 3xUN в конце (Task 5+ переиспользуют его).
#
# Запуск (из scylladb/):
#   bash ops/repair-demo.sh | tee ../scratchout/repair.txt
# Требует: живой 3-узловой кластер (docker compose -f compose/compose.yml up -d),
# схему telemetry (dataset/schema.cql) — repair-demo.sh идемпотентно досоздаёт
# readings_twcs сам, если стенд compaction ещё не запускался.

set -uo pipefail
# На Git Bash/MSYS аргументы вида /repair-demo-divergence.cql (путь ВНУТРИ
# контейнера) иначе мангаются в windows-путь хоста; на Linux/WSL переменная
# безвредна (см. тот же приём в ops/verify-static.sh).
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> scylladb/
COMPOSE="compose/compose.yml"
MARKER_DEVICE="dev-repair-demo-0001"
MARKER_DAY="2026-07-06"

step() { echo; echo "=== $* ==="; }

step "0/6 Базовое состояние кластера (до демонстрации)"
docker exec scylla1 nodetool status

step "0/6 Схема readings_twcs (idempotent CREATE — на случай отдельного запуска без стенда compaction)"
docker exec scylla1 cqlsh -e "CREATE TABLE IF NOT EXISTS telemetry.readings_twcs (device_id text, day date, event_time timestamp, metric text, value double, region text, status text, PRIMARY KEY ((device_id, day), event_time)) WITH CLUSTERING ORDER BY (event_time DESC) AND compaction = {'class':'TimeWindowCompactionStrategy','compaction_window_size':'1','compaction_window_unit':'DAYS'}"

step "1/6 Остановить scylla3 (снять из кворума)"
docker compose -f "$COMPOSE" stop scylla3
echo "Ждём, пока gossip увидит scylla3 как DN (до 60с)..."
for i in $(seq 1 20); do
  UN_COUNT=$(docker exec scylla1 nodetool status 2>/dev/null | grep -cE '^UN')
  if [ "$UN_COUNT" = "2" ]; then break; fi
  sleep 3
done
docker exec scylla1 nodetool status

step "2/6 Писать при 2/3 узлов живых (QUORUM=2 из 3 всё ещё выполним; scylla3 эти записи пропускает -> реальное расхождение реплик, координаторы queue'ят hint для scylla3)"
echo "Генерируем объёмную запись (500 партиций x 20 строк = 10000 строк, сутки $MARKER_DAY) -- маленький маркер (3 строки)"
echo "у ScyllaDB на локальном docker-compose реплицируется через hinted handoff быстрее, чем успевает выполниться этот скрипт;"
echo "объём нужен, чтобы поймать расхождение ДО того, как hint успеет догнать scylla3."
# ОТНОСИТЕЛЬНЫЙ путь (без ведущего /), НЕ mktemp: mktemp кладёт файл в
# MSYS-приватный /tmp, который docker.exe (нативный Windows-бинарь) на
# Git Bash резолвит НЕ туда (проверено живьём: получили `G:\tmp\tmp.XXXX:
# The system cannot find the file specified` от `docker cp`). Относительный
# путь в текущей (реальной, не MSYS-виртуальной) директории резолвится
# одинаково что bash'ем, что docker.exe.
DIVFILE="repair-demo-divergence.cql"
for d in $(seq 0 499); do
  dev=$(printf "%s-%05d" "$MARKER_DEVICE" "$d")
  echo "BEGIN UNLOGGED BATCH"
  for i in $(seq 0 19); do
    printf "  INSERT INTO telemetry.readings_twcs (device_id, day, event_time, metric, value, region, status) VALUES ('%s','%s','%sT%02d:00:00Z','cpu',99.9,'eu-west','ok');\n" \
      "$dev" "$MARKER_DAY" "$MARKER_DAY" "$i"
  done
  echo "APPLY BATCH;"
done > "$DIVFILE"
docker cp "$DIVFILE" scylla1:/repair-demo-divergence.cql
docker exec scylla1 cqlsh -f /repair-demo-divergence.cql >/dev/null
rm -f "$DIVFILE"
echo "Записано 10000 строк (500 партиций x 20) при scylla3 down (coordinator: scylla1, CL по умолчанию)."

step "3/6 Поднять scylla3"
docker compose -f "$COMPOSE" start scylla3
echo "Ждём кворума 3xUN (до 3 минут)..."
for i in $(seq 1 36); do
  UN_COUNT=$(docker exec scylla1 nodetool status 2>/dev/null | grep -cE '^UN')
  if [ "$UN_COUNT" = "3" ]; then break; fi
  sleep 5
done
docker exec scylla1 nodetool status

step "4/6 СРАЗУ ПОСЛЕ подъёма, ДО repair: что видит САМ scylla3 своими данными (CONSISTENCY ONE — только локальная реплика scylla3, без координации с scylla1/scylla2; 3 выборочные партиции, каждая ожидает 20 строк если полностью реплицирована)"
for d in 00000 00250 00499; do
  dev="${MARKER_DEVICE}-${d}"
  echo -n "  $dev: "
  docker exec scylla3 cqlsh -e "CONSISTENCY ONE; SELECT count(*) FROM telemetry.readings_twcs WHERE device_id='$dev' AND day='$MARKER_DAY';" 2>&1 | grep -A2 "^ count$" | tail -1 | tr -d ' '
done
echo "(ожидание строк на партицию: 20, если hinted handoff уже успел; меньше 20 -- реальное расхождение, честно фиксируем то, что покажет живой кластер, см. README «Стенд #3»)"

step "5/6 nodetool cluster repair --keyspace telemetry (tablet keyspace -- nodetool repair -pr НЕ подходит, см. заголовок файла)"
docker exec scylla1 nodetool cluster repair --keyspace telemetry

step "6/6 ПОСЛЕ repair: что видит scylla3 теперь (CONSISTENCY ONE), те же 3 партиции"
for d in 00000 00250 00499; do
  dev="${MARKER_DEVICE}-${d}"
  echo -n "  $dev: "
  docker exec scylla3 cqlsh -e "CONSISTENCY ONE; SELECT count(*) FROM telemetry.readings_twcs WHERE device_id='$dev' AND day='$MARKER_DAY';" 2>&1 | grep -A2 "^ count$" | tail -1 | tr -d ' '
done
echo "(ожидание: 20 на партицию -- repair синхронизировал scylla3 с остальными репликами)"

step "Финальное состояние кластера (должно быть 3xUN -- Task 5+ переиспользуют этот кластер)"
docker exec scylla1 nodetool status

echo
echo "Готово."
