#!/usr/bin/env bash
# ops-demo.sh — Стенд #7 серии "ScyllaDB: глубокое погружение", живая
# демонстрация backup/restore (nodetool snapshot + nodetool refresh) НА
# СКРАТЧ-ТАБЛИЦЕ telemetry.ops_backup_demo и Alternator (DynamoDB API) на
# ОТДЕЛЬНОМ транзитном узле. Синхронный: каждый шаг печатает результат ДО
# следующего. НИКОГДА не трогает telemetry.readings (672000 строк, Task 1) и
# НИКОГДА не пересоздаёт scylla1/2/3 (у compose/compose.yml нет именованного
# volume для данных — recreate = потеря датасета).
#
# -- backup/restore: ПОЧЕМУ scratch-таблица, а не readings ------------------
# `nodetool snapshot`/`nodetool refresh` — это НЕ point-in-time snapshot API
# уровня строк: восстановление подменяет sstable-файлы ЦЕЛОЙ таблицы. Чтобы
# честно продемонстрировать цикл "удалили -> восстановили -> count совпал"
# без риска для основного датасета серии, скрипт создаёт отдельную маленькую
# таблицу telemetry.ops_backup_demo (50 строк), делает снапшот ЕЁ, удаляет ЕЁ
# данные (TRUNCATE), восстанавливает ИЗ ЕЁ ЖЕ снапшота. `readings` в этом
# скрипте НИ РАЗУ не упоминается в DML/DDL.
#
# -- restore-механизм: nodetool refresh, НЕ sstableloader --------------------
# Проверено живьём (см. README «Стенд #7»): `sstableloader` в этом образе
# требует отдельного JMX/сетевого клиента и полноценного stream-протокола
# между "источником" и живым кластером — избыточно для single-node
# restore-из-своего-же-снапшота. Рабочий и куда более простой путь для этого
# случая: снапшот таблицы физически лежит В ТОЙ ЖЕ директории данных
# (`data/telemetry/<table>-<uuid>/snapshots/<tag>/`), поэтому достаточно
# скопировать sstable-файлы снапшота в `upload/` этой же директории и вызвать
# `nodetool refresh --keyspace telemetry --table ops_backup_demo` — штатная
# nodetool-команда "загрузить sstable без рестарта", описанная в самом
# nodetool help refresh для восстановления из бэкапа/снапшота. Это НЕ
# восстановление НА ДРУГОЙ узел/кластер (для этого действительно нужен
# sstableloader или `nodetool backup` в object storage — вне рамок этого
# демо-стенда, честно отмечено в README).
#
# -- Alternator: почему отдельный транзитный контейнер -----------------------
# `--alternator-port` — флаг ЗАПУСКА узла (ScyllaDB), не runtime-toggle.
# Основной кластер (scylla1/2/3) поднят БЕЗ этого флага (см.
# compose/compose.yml) — редактировать command и делать `up -d` означало бы
# recreate контейнеров => потеря 672000 строк readings (нет volume). Поэтому
# Alternator демонстрируется на ОТДЕЛЬНОМ, ОДНОНОДОВОМ, ТРАНЗИТНОМ контейнере
# `scylla-alt` на ТОЙ ЖЕ сети `scylla-cookbook-net` — поднимается и удаляется
# ЭТИМ скриптом, основного кластера не касается вообще.
#
# Запуск (из scylladb/):
#   bash ops/ops-demo.sh | tee ../scratchout/ops.txt
# Требует: живой 3-узловой кластер (docker compose -f compose/compose.yml up -d),
# схему telemetry (dataset/schema.cql), собранный ops-stand/ (go build ./...
# внутри golang:1.26 либо локально go1.26+), docker.

set -uo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."   # -> scylladb/
ROOT="$(pwd)"
COMPOSE="compose/compose.yml"

step() { echo; echo "=== $* ==="; }
fail=0

# =============================================================================
# ЧАСТЬ A — Backup/restore на telemetry.ops_backup_demo
# =============================================================================

step "A0/A8 Базовое состояние кластера (до демонстрации)"
docker exec scylla1 nodetool status

step "A1/A8 Создать scratch-таблицу telemetry.ops_backup_demo и загрузить 50 строк (идемпотентно — DROP/CREATE)"
docker exec scylla1 cqlsh -e "DROP TABLE IF EXISTS telemetry.ops_backup_demo;"
docker exec scylla1 cqlsh -e "CREATE TABLE telemetry.ops_backup_demo (id text PRIMARY KEY, payload text);"
LOADFILE="ops-backup-demo-load.cql"
{
  echo "BEGIN UNLOGGED BATCH"
  for i in $(seq 0 49); do
    printf "INSERT INTO telemetry.ops_backup_demo (id, payload) VALUES ('bk-%05d', 'payload-data-%05d');\n" "$i" "$i"
  done
  echo "APPLY BATCH;"
} > "$LOADFILE"
docker cp "$LOADFILE" scylla1:/ops-backup-demo-load.cql
docker exec scylla1 cqlsh -f /ops-backup-demo-load.cql
rm -f "$LOADFILE"
# cqlsh -e "SELECT count(*) ..." печатает: пустая строка / " count" /
# "--------" / " 672000" / пустая / "(1 rows)" -- значение ВСЕГДА на 4-й
# строке, не на 3-й (там разделитель). Проверено живьём построчно (cat -A).
ORIGINAL_COUNT=$(docker exec scylla1 cqlsh -e "SELECT count(*) FROM telemetry.ops_backup_demo;" 2>&1 | sed -n '4p' | tr -d ' ')
echo "ops_backup_demo: загружено, count=$ORIGINAL_COUNT (ожидание: 50)"

step "A2/A8 nodetool flush (sstable на диске ДО снапшота) + определить директорию таблицы"
docker exec scylla1 nodetool flush telemetry ops_backup_demo
# Директория таблицы на диске = "<table>-<cf_id без дефисов>". Берём cf_id
# АВТОРИТЕТНО из system_schema.tables (не через `ls .../telemetry/ | grep`) —
# живая находка: DROP TABLE НЕ удаляет физическую директорию немедленно
# (ScyllaDB держит её каким-то временем/до compaction/GC), поэтому от
# предыдущих прогонов этого же скрипта на диске могут оставаться "осиротевшие"
# ops_backup_demo-<старый-uuid> директории -- `grep` совпал бы с несколькими
# и сломал бы путь; system_schema.tables отдаёт РОВНО текущий, живой id.
CF_ID=$(docker exec scylla1 cqlsh -e "SELECT id FROM system_schema.tables WHERE keyspace_name='telemetry' AND table_name='ops_backup_demo';" 2>&1 | sed -n '4p' | tr -d ' \r')
TBLDIR="ops_backup_demo-${CF_ID//-/}"
echo "cf_id: $CF_ID"
echo "директория таблицы: data/telemetry/$TBLDIR"

step "A3/A8 nodetool snapshot telemetry.ops_backup_demo (тег backupdemo1)"
docker exec scylla1 nodetool snapshot --keyspace-table-list telemetry.ops_backup_demo -t backupdemo1
SNAP_FILES=$(docker exec scylla1 sh -c "ls /var/lib/scylla/data/telemetry/$TBLDIR/snapshots/backupdemo1/ | wc -l" | tr -d '\r')
SNAP_SIZE=$(docker exec scylla1 sh -c "du -sh /var/lib/scylla/data/telemetry/$TBLDIR/snapshots/backupdemo1/ | cut -f1" | tr -d '\r')
echo "snapshot path: data/telemetry/$TBLDIR/snapshots/backupdemo1/"
echo "snapshot files: $SNAP_FILES"
echo "snapshot size:  $SNAP_SIZE"
docker exec scylla1 nodetool listsnapshots | grep -E "backupdemo1|Snapshot name"

step "A4/A8 Удалить данные ops_backup_demo (TRUNCATE — симуляция потери данных)"
docker exec scylla1 cqlsh -e "TRUNCATE telemetry.ops_backup_demo;"
AFTER_DELETE_COUNT=$(docker exec scylla1 cqlsh -e "SELECT count(*) FROM telemetry.ops_backup_demo;" 2>&1 | sed -n '4p' | tr -d ' ')
echo "после TRUNCATE: count=$AFTER_DELETE_COUNT (ожидание: 0)"

step "A5/A8 Restore: скопировать sstable снапшота в upload/, nodetool refresh (см. заголовок файла — почему refresh, не sstableloader)"
docker exec scylla1 sh -c "mkdir -p /var/lib/scylla/data/telemetry/$TBLDIR/upload && cp /var/lib/scylla/data/telemetry/$TBLDIR/snapshots/backupdemo1/*-big-*.db /var/lib/scylla/data/telemetry/$TBLDIR/snapshots/backupdemo1/*-big-*.crc32 /var/lib/scylla/data/telemetry/$TBLDIR/snapshots/backupdemo1/*-big-*.txt /var/lib/scylla/data/telemetry/$TBLDIR/upload/"
UPLOAD_FILES=$(docker exec scylla1 sh -c "ls /var/lib/scylla/data/telemetry/$TBLDIR/upload/ | wc -l" | tr -d '\r')
echo "скопировано файлов в upload/: $UPLOAD_FILES"
docker exec scylla1 nodetool refresh --keyspace telemetry --table ops_backup_demo

step "A6/A8 Проверка восстановления"
RESTORED_COUNT=$(docker exec scylla1 cqlsh -e "SELECT count(*) FROM telemetry.ops_backup_demo;" 2>&1 | sed -n '4p' | tr -d ' ')
echo "после restore: count=$RESTORED_COUNT"
echo "original_count=$ORIGINAL_COUNT restore_count=$RESTORED_COUNT"
if [ "$ORIGINAL_COUNT" = "$RESTORED_COUNT" ] && [ -n "$ORIGINAL_COUNT" ]; then
  echo "ASSERT OK: restore_count == original_count ($RESTORED_COUNT)"
else
  echo "ASSERT FAIL: restore_count ($RESTORED_COUNT) != original_count ($ORIGINAL_COUNT)"
  fail=1
fi

step "A7/A8 Уборка демо-снапшота и scratch-таблицы (не влияет на readings)"
docker exec scylla1 nodetool clearsnapshot -t backupdemo1 --keyspace telemetry
docker exec scylla1 cqlsh -e "DROP TABLE IF EXISTS telemetry.ops_backup_demo;"
echo "ops_backup_demo и снапшот backupdemo1 удалены"

step "A8/A8 Контроль: readings НЕ тронут"
READINGS_COUNT=$(docker exec scylla1 cqlsh -e "SELECT count(*) FROM telemetry.readings;" 2>&1 | sed -n '4p' | tr -d ' ')
echo "telemetry.readings count=$READINGS_COUNT (ожидание: 672000, неизменно)"
if [ "$READINGS_COUNT" != "672000" ]; then
  echo "ASSERT FAIL: readings count изменился!"
  fail=1
fi

# =============================================================================
# ЧАСТЬ B — Alternator (DynamoDB API) на транзитном узле scylla-alt
# =============================================================================

step "B1/B4 Поднять транзитный однонодовый scylla-alt (--alternator-port 8000), НЕ трогая основной кластер"
docker rm -f scylla-alt >/dev/null 2>&1 || true
docker run -d --name scylla-alt --network scylla-cookbook-net \
  scylladb/scylla:2026.2.0 \
  --alternator-port 8000 --alternator-write-isolation always \
  --smp 1 --memory 2G --overprovisioned 1 --api-address 0.0.0.0
echo "scylla-alt запущен, ждём готовности Alternator-порта (до 90с)..."
# Пробник — ОТДЕЛЬНЫЙ curl-контейнер на той же сети, не `docker exec
# scylla-alt curl localhost:8000` — тот же 127.0.0.1-connection-refused
# паттерн, что и у :9180/metrics (см. README «Стенд #2»/«Стенд #7»): REST/API
# порты ScyllaDB слушают на сетевом адресе контейнера, не на loopback.
READY=0
for i in $(seq 1 45); do
  if docker run --rm --network scylla-cookbook-net curlimages/curl -s -o /dev/null -m 2 http://scylla-alt:8000/ 2>/dev/null; then
    READY=1
    break
  fi
  sleep 2
done
if [ "$READY" = "1" ]; then
  echo "Alternator-порт отвечает (попытка $i, ~$((i*2))с)"
else
  echo "Alternator-порт НЕ ответил за 90с — сценарий alternator ниже сообщит об этом честно"
fi

step "B2/B4 go build ops-stand/ (golang:1.26, контейнер — тот же паттерн, что verify-static.sh)"
mkdir -p .gocache
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/ops-stand golang:1.26 \
  sh -c 'go build -o /tmp/ops-stand . && echo BUILD_OK'

step "B3/B4 ops-stand -scenario alternator (контейнер на scylla-cookbook-net, endpoint http://scylla-alt:8000)"
docker run --rm -v "$ROOT:/app" -v "$ROOT/.gocache:/go/pkg/mod" -w /app/ops-stand \
  --network scylla-cookbook-net \
  -e ALTERNATOR_ENDPOINT=http://scylla-alt:8000 \
  golang:1.26 \
  sh -c 'go run . -scenario alternator'
ALT_EXIT=$?
if [ "$ALT_EXIT" -ne 0 ]; then
  echo "ASSERT FAIL: ops-stand -scenario alternator завершился с кодом $ALT_EXIT"
  fail=1
fi

step "B4/B4 Снести транзитный scylla-alt (основной кластер не затронут)"
docker rm -f scylla-alt >/dev/null 2>&1 || echo "(scylla-alt уже отсутствует)"
echo "scylla-alt удалён"

step "Финальное состояние основного кластера (должно быть 3xUN, readings=672000 — Task 9 переиспользует)"
docker exec scylla1 nodetool status
docker exec scylla1 cqlsh -e "SELECT count(*) FROM telemetry.readings;"

echo
if [ "$fail" -eq 0 ]; then
  echo "ВСЁ ЗЕЛЁНОЕ ✓ — backup/restore + Alternator пройдены, readings не тронут."
else
  echo "ЕСТЬ ПРОВАЛЫ ✗ — см. FAIL выше."
  exit 1
fi
