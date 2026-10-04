#!/usr/bin/env bash
# Автоматический restore-тест: доказывает, что из бэкапа РЕАЛЬНО поднимается
# нужное состояние, и замеряет фактическое время восстановления и фактическую
# потерю данных. Главная мысль статьи — «бэкап без проверки восстановления не
# бэкап» — здесь превращена в число.
#
# Сценарий катастрофы: полный бэкап → ещё записи (только в WAL) → DROP TABLE
# (человеческая ошибка / шифровальщик) → PITR-восстановление на точку перед
# катастрофой.
#
# ТЕРМИНОЛОГИЯ (важно, её часто путают):
#   RTO/RPO — это ЦЕЛИ (objectives), которые назначает бизнес: «за сколько обязаны
#   восстановиться» и «до какой точки во времени обязаны восстановить данные».
#   Скрипт целей не выдумывает — он меряет ФАКТ: сколько заняло восстановление
#   (сравнивать с целевым RTO) и сколько данных потерялось бы (сравнивать с целевым RPO).
set -euo pipefail
cd "$(dirname "$0")"

RESTORE_VOLUME="dr-restore-data"
RESTORE_CONTAINER="dr-restore"
RESTORE_PORT=5445
PGDATA_IN=/var/lib/postgresql/18/docker # путь data-dir у образа postgres:18
ARCHIVE_IN=/var/lib/postgresql/archive  # куда archive_command кладёт сегменты
RESTORE_TMP="$(mktemp -d)"

PSQL() { docker compose exec -T postgres psql -U postgres -d drdemo -qtAc "$1" | tr -d '\r'; }
RPSQL() { docker exec "$RESTORE_CONTAINER" psql -U postgres -d drdemo -qtAc "$1" | tr -d '\r'; }
die() { echo "❌ $*" >&2; exit 1; }

cleanup() {
  docker rm -f "$RESTORE_CONTAINER" >/dev/null 2>&1 || true
  docker volume rm -f "$RESTORE_VOLUME" >/dev/null 2>&1 || true
  rm -rf "$RESTORE_TMP"
  docker compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

ARCHIVE_VOLUME="dr-archive" # named volume из docker-compose.yml (не host-каталог)

echo "=== 1. Поднимаем primary с архивированием WAL ==="
docker compose up -d
until docker compose exec -T postgres pg_isready -U postgres -q 2>/dev/null; do sleep 1; done

# ГОТЧА (ловил ревьюер, дважды). Первая версия монтировала архив с хоста: postgres
# в контейнере не мог туда писать (чужой uid), archive_command падал МОЛЧА, архив
# оставался пустым и восстановление не доходило до цели. Починили chown'ом — и
# получили вторую беду: каталог на хосте оставался с владельцем uid 999 и режимом
# 0700, поэтому ПОВТОРНЫЙ запуск падал на «Permission denied». Теперь архив — named
# volume: владелец живёт внутри Docker, хост не трогаем, а down -v в cleanup даёт
# чистый старт каждому прогону. Владельца всё равно выставляем явно: пустой том
# создаётся root'ом, писать в него postgres не смог бы.
docker compose exec -u root -T postgres sh -c \
  "mkdir -p $ARCHIVE_IN && chown postgres:postgres $ARCHIVE_IN && chmod 0700 $ARCHIVE_IN"
docker compose exec -T postgres sh -c "test -w $ARCHIVE_IN" \
  || die "каталог архива недоступен postgres на запись — archive_command молча не сработает"

# Сегменты прошлых прогонов не мешают: cleanup удаляет том целиком (down -v),
# поэтому каждый запуск стартует с пустого архива. Подстраховка на случай
# прерванного прогона:
docker compose exec -u root -T postgres sh -c "rm -f $ARCHIVE_IN/*" || true

echo "=== 2. Данные + полный бэкап (pg_basebackup) ==="
PSQL "CREATE TABLE orders(id serial primary key, ts timestamptz default now());"
PSQL "INSERT INTO orders SELECT FROM generate_series(1,100);" # 100 заказов ДО бэкапа
AT_BACKUP=$(PSQL "SELECT count(*) FROM orders;")
docker compose exec -T postgres bash -c "rm -rf /tmp/base && pg_basebackup -U postgres -D /tmp/base -X stream"
echo "  на момент бэкапа: $AT_BACKUP заказов"

echo "=== 3. Ещё записи ПОСЛЕ бэкапа (живут только в WAL) ==="
PSQL "INSERT INTO orders SELECT FROM generate_series(101,150);" # +50, их спасёт только WAL
AT_DISASTER=$(PSQL "SELECT count(*) FROM orders;")
sleep 1
TARGET=$(PSQL "SELECT now();") # точка восстановления: сразу перед катастрофой
sleep 1

echo "=== 4. КАТАСТРОФА: DROP TABLE (человеческая ошибка / шифровальщик) ==="
PSQL "DROP TABLE orders;"
PSQL "SELECT pg_switch_wal();" >/dev/null # форсируем архивацию сегмента со всеми коммитами
sleep 4

# Fail-loud: если архив пуст, дальше мерить нечего — молча «восстановиться» нельзя.
SEGMENTS=$(docker compose exec -T postgres sh -c "ls -1 $ARCHIVE_IN | wc -l" | tr -d '\r ')
if [ "${SEGMENTS:-0}" -lt 1 ]; then
  echo "--- archive_command из лога primary:" >&2
  docker compose logs postgres 2>&1 | grep -iE "archive" | tail -5 >&2 || true
  die "WAL-архив пуст ($SEGMENTS сегментов) — PITR невозможен. Проверьте права на каталог архива."
fi
echo "  сегментов в архиве: $SEGMENTS; на момент катастрофы было $AT_DISASTER заказов"

echo "=== 5. RESTORE-ТЕСТ: PITR на точку перед катастрофой ==="
# Отсчёт времени начинается ЗДЕСЬ — с решения восстанавливаться, а не со старта
# контейнера: доставание бэкапа, подготовка recovery-конфига и тома тоже входят
# в фактическое время восстановления.
RTO_START=$(date +%s.%N)
CID=$(docker compose ps -q postgres)
STAGE="$RESTORE_TMP/base"
mkdir -p "$STAGE"
docker cp "$CID:/tmp/base/." "$STAGE/"
touch "$STAGE/recovery.signal"
cat >> "$STAGE/postgresql.auto.conf" <<EOF
restore_command = 'cp /archive/%f %p'
recovery_target_time = '$TARGET'
recovery_target_action = 'promote'
EOF

docker volume create "$RESTORE_VOLUME" >/dev/null
# И data-dir, и архив — named volumes: unix-овнершип живёт внутри Docker, на хосте
# ничего не портится, и повторный запуск скрипта не упирается в чужие права.
MSYS_NO_PATHCONV=1 docker create --name "$RESTORE_CONTAINER" \
  -p "${RESTORE_PORT}:5432" \
  -v "$RESTORE_VOLUME:$PGDATA_IN" \
  -v "$ARCHIVE_VOLUME:/archive:ro" \
  postgres:18.4 \
  bash -c "cp -a /tmp/dr-stage/. $PGDATA_IN/ && rm -rf /tmp/dr-stage && chown -R postgres:postgres $PGDATA_IN && chmod 0700 $PGDATA_IN && exec gosu postgres postgres" >/dev/null
docker cp "$STAGE" "$RESTORE_CONTAINER:/tmp/dr-stage"
docker start "$RESTORE_CONTAINER" >/dev/null

ready=0
for _ in $(seq 1 120); do
  if docker exec "$RESTORE_CONTAINER" pg_isready -U postgres -q 2>/dev/null \
     && [ "$(RPSQL 'SELECT pg_is_in_recovery();')" = "f" ]; then
    ready=1; break
  fi
  sleep 0.5
done
RECOVERY_SECONDS=$(awk "BEGIN{printf \"%.1f\", $(date +%s.%N) - $RTO_START}")
if [ "$ready" != 1 ]; then
  echo "--- лог restore-инстанса:" >&2
  docker logs "$RESTORE_CONTAINER" 2>&1 | tail -15 >&2
  die "восстановление не завершилось промоутом за отведённое время"
fi

echo "=== 6. Проверка восстановления (без неё бэкап — лишь предположение) ==="
RESTORED=$(RPSQL "SELECT count(*) FROM orders;")
[ "$RESTORED" = "$AT_DISASTER" ] || die "восстановлено $RESTORED заказов, ждали $AT_DISASTER"
echo "  ✅ таблица поднята, DROP отменён, все $RESTORED заказов на месте, инстанс промоутнут"

LOST_WITH_PITR=$((AT_DISASTER - RESTORED))
LOST_BACKUP_ONLY=$((AT_DISASTER - AT_BACKUP))
echo
echo "############ ИТОГ restore-теста ############"
echo "  фактическое время восстановления: ${RECOVERY_SECONDS} с   ← сравнивать с целевым RTO; машинозависимо"
echo "  достигнутая точка восстановления: момент перед катастрофой"
echo "  фактическая потеря данных с PITR:            $LOST_WITH_PITR заказов"
echo "  фактическая потеря, будь только полный бэкап: $LOST_BACKUP_ONLY заказов"
echo "  ↑ непрерывный WAL-архив приближает достижимую точку восстановления к моменту"
echo "    катастрофы: потеря падает с $LOST_BACKUP_ONLY заказов до $LOST_WITH_PITR."
