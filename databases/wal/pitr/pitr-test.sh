#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

PG_COMPOSE="../postgres/docker-compose.yml"
ARCHIVE_DIR="$(cd ../postgres/archive && pwd)"
# ГОТЧА (см. README): data-dir restore-инстанса раньше жил на host-bind
# (./restore-base под репозиторием, на Windows/WSL-смонтированной ФС) — PostgreSQL
# требует unix-овнершип на data-каталоге (chown postgres:postgres), а такой bind
# его не поддерживает надёжно (FATAL: data directory ... has wrong ownership).
# Теперь data-dir — именованный Docker volume (реальная ФС внутри Docker/WSL2 VM,
# полноценный unix-овнершип). Host-bind остаётся только для archive/, и то read-only
# (чтение файла не требует смены владельца, в отличие от data-каталога).
RESTORE_VOLUME="pitr-restore-data"
RESTORE_CONTAINER="pitr-restore"
RESTORE_PORT=5443
# Временная директория для staging-копии базового бэкапа (+ recovery.signal,
# postgresql.auto.conf), которая затем заливается в volume через `docker cp`.
# Это НЕ data-dir и не долгоживущий bind-mount для PostgreSQL — обычный
# host-каталог для промежуточного хранения байт между двумя `docker cp`,
# к которому требование unix-овнершипа data-каталога не относится.
RESTORE_TMP="$(mktemp -d)"

PSQL(){ docker compose -f "$PG_COMPOSE" exec -T postgres psql -U postgres -d waldemo -qtAc "$1"; }

cleanup_restore() {
  docker rm -f "$RESTORE_CONTAINER" >/dev/null 2>&1 || true
  docker volume rm -f "$RESTORE_VOLUME" >/dev/null 2>&1 || true
  rm -rf "$RESTORE_TMP"
}
trap cleanup_restore EXIT

echo "=== поднимаем primary (archive_mode=on из стенда ../postgres/) ==="
(cd ../postgres && docker compose up -d)
sleep 8

# ГОТЧА: archive/ — общий том стенда ../postgres/, между прогонами в нём остаются
# сегменты прошлых сессий. archive_command вида
#   test ! -f /var/lib/postgresql/archive/%f && cp %p ...
# архивирует СТРОГО по порядку: если %f уже существует, команда возвращает
# exit 1 и архивация "залипает" на этом сегменте навсегда (retry каждые ~60с),
# так и не добравшись до свежих сегментов с нашими данными. Поэтому чистим
# архив перед прогоном — это demo-артефакт, не данные.
echo "=== очищаем archive/ от сегментов прошлых прогонов (см. README про подводные камни) ==="
rm -f "$ARCHIVE_DIR"/*

PSQL "DROP TABLE IF EXISTS pitr; CREATE TABLE pitr(id int, ts timestamptz default now());"

echo "=== базовый бэкап primary (pg_basebackup -X stream) ==="
docker compose -f "$PG_COMPOSE" exec -T postgres bash -c "rm -rf /tmp/base && pg_basebackup -U postgres -D /tmp/base -X stream"

PSQL "INSERT INTO pitr(id) VALUES (1);"
sleep 2
TARGET=$(PSQL "SELECT now();" | tr -d '\r')
echo "recovery_target_time = $TARGET (после id=1, до id=2)"
sleep 2
PSQL "INSERT INTO pitr(id) VALUES (2);"
PSQL "SELECT pg_switch_wal();" >/dev/null   # форсируем архивацию сегмента с обоими коммитами
sleep 5
echo "текущее состояние primary (ожидаем 2 строки): $(PSQL 'SELECT count(*) FROM pitr;')"

echo "=== содержимое archive/ после pg_switch_wal ==="
ls -la "$ARCHIVE_DIR"

echo "=== копируем базовый бэкап из контейнера primary в staging-каталог на хосте ==="
# $RESTORE_TMP/base — чисто транзитный хоп для потока байт (docker cp не умеет
# контейнер->контейнер напрямую: "copying between containers is not supported").
# Это НЕ data-dir PostgreSQL — овнершип этого промежуточного каталога роли не
# играет, реальный chown происходит внутри контейнера (на volume) при старте.
CID=$(docker compose -f "$PG_COMPOSE" ps -q postgres)
RESTORE_STAGE="$RESTORE_TMP/base"
mkdir -p "$RESTORE_STAGE"
docker cp "$CID:/tmp/base/." "$RESTORE_STAGE/"

echo "=== recovery.signal + recovery-настройки прямо в staging-каталоге ==="
touch "$RESTORE_STAGE/recovery.signal"
cat >> "$RESTORE_STAGE/postgresql.auto.conf" <<EOF
restore_command = 'cp /archive/%f %p'
recovery_target_time = '$TARGET'
recovery_target_action = 'promote'
EOF

echo "=== готовим restore: data-dir — Docker named volume (не host-bind, см. README) ==="
docker rm -f "$RESTORE_CONTAINER" >/dev/null 2>&1 || true
docker volume rm -f "$RESTORE_VOLUME" >/dev/null 2>&1 || true
docker volume create "$RESTORE_VOLUME" >/dev/null

echo "=== создаём (пока не запуская) restore-контейнер: volume под data-dir, archive/ read-only ==="
# Git Bash на Windows переписывает пути вида "G:/..." в "-v" аргументах docker
# (MSYS path conversion), из-за чего bind-том archive/ мапится не туда (см. README).
# MSYS_NO_PATHCONV=1 отключает эту конвертацию — нужен только для этого
# конкретного docker create (docker cp ниже его не требует и ломается от него).
# Само имя volume ("$RESTORE_VOLUME") путём не является и не страдает от конвертации.
# CMD контейнера сам разворачивает staging-каталог (см. ниже) поверх volume —
# это делает обычный `cp -a` ВНУТРИ контейнера, а не docker cp/MSYS: так надёжнее,
# `docker cp SRC/. CONTAINER:/dest/` на Git Bash теряет трейлинг-точку при конвертации
# хостового пути и вместо flatten-копии кладёт вложенную папку (проверено вживую).
# Staging кладём в /tmp/pitr-stage (ephemeral-слой контейнера, НЕ под volume) —
# кладя его прямо под $PGDATA, поймали коллизию имён: базовый бэкап сам содержит
# top-level каталог "base" (табличное пространство по умолчанию), и если staging-
# каталог тоже назвать/положить как ".../docker/base", `cp -a base/. ..` копирует
# вложенный "base" сам на себя и последующий rm -rf сносит уже скопированные данные
# (проверено вживую: recovery проходит, но потом "base/16384 ... PG_VERSION missing").
MSYS_NO_PATHCONV=1 docker create --name "$RESTORE_CONTAINER" \
  --network postgres_default \
  -p "${RESTORE_PORT}:5432" \
  -v "$RESTORE_VOLUME:/var/lib/postgresql/18/docker" \
  -v "$ARCHIVE_DIR:/archive:ro" \
  postgres:18.4 \
  bash -c 'cp -a /tmp/pitr-stage/. /var/lib/postgresql/18/docker/ && rm -rf /tmp/pitr-stage && chown -R postgres:postgres /var/lib/postgresql/18/docker && chmod 0700 /var/lib/postgresql/18/docker && exec gosu postgres postgres' >/dev/null

echo "=== заливаем staging-каталог в контейнер (docker cp host -> container, ещё не запущен) ==="
# /tmp/pitr-stage ещё не существует в контейнере — docker cp копирует туда
# содержимое staging-каталога один в один, разворачивание поверх volume ($PGDATA)
# делает CMD контейнера выше (уже внутри контейнера, без коллизий имён).
docker cp "$RESTORE_STAGE" "$RESTORE_CONTAINER:/tmp/pitr-stage"

echo "=== стартуем restore-инстанс (flatten + chown + promote — см. CMD контейнера) ==="
docker start "$RESTORE_CONTAINER" >/dev/null

echo "=== ждём завершения recovery (до promote) ==="
for i in $(seq 1 30); do
  if docker exec "$RESTORE_CONTAINER" pg_isready -U postgres -q 2>/dev/null; then
    break
  fi
  sleep 1
done
sleep 2

echo "=== лог restore-инстанса (recovery target / promote) ==="
docker logs "$RESTORE_CONTAINER" 2>&1 | grep -iE "recovery|redo|archive|promot|consistent" || true

echo "=== ассерт: на восстановленном инстансе видно только id=1 ==="
docker exec "$RESTORE_CONTAINER" psql -U postgres -d waldemo -qtAc "SELECT * FROM pitr ORDER BY id;"
echo "count(*) на восстановленном инстансе (ожидаем 1): $(docker exec "$RESTORE_CONTAINER" psql -U postgres -d waldemo -qtAc 'SELECT count(*) FROM pitr;')"
echo "pg_is_in_recovery() (ожидаем f — промоутнулся): $(docker exec "$RESTORE_CONTAINER" psql -U postgres -d waldemo -qtAc 'SELECT pg_is_in_recovery();')"

echo "=== останавливаем restore-инстанс и удаляем volume (отсоединяем от сети перед down primary) ==="
docker rm -f "$RESTORE_CONTAINER" >/dev/null 2>&1 || true
docker volume rm -f "$RESTORE_VOLUME" >/dev/null 2>&1 || true

echo "=== останавливаем primary ==="
(cd ../postgres && docker compose down)
