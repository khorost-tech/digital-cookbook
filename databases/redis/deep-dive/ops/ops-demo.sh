#!/usr/bin/env bash
# Стенд #7: backup/restore-цикл через `redis-cli --rdb` (живой снапшот по
# сети через SYNC, без остановки сервиса и без docker exec внутрь
# контейнера — порт 6379 уже опубликован compose/base.yml) + docker volume.
#
# Использование:
#   ./ops/ops-demo.sh redis:8.8
#   ./ops/ops-demo.sh valkey/valkey:8.1
#
# Цикл:
#   1. Свежий redis-master, сеет USERS x EVENTS_PER_USER событий (dataset/).
#   2. `redis-cli -h redis-master --rdb <файл>` из одноразового контейнера
#      на сети redis-cookbook-net — снимает живой RDB-снапшот через SYNC.
#   3. `down -v` — уничтожает именованный volume целиком (не просто
#      останавливает контейнер).
#   4. ДОКАЗАТЕЛЬСТВО, что данные реально исчезли: свежий redis-master на
#      новом volume (тот же путь down→up, что и restore ниже, но БЕЗ
#      восстановления файла) — DBSIZE и точечный ключ ОБЯЗАНЫ отсутствовать,
#      иначе скрипт фатально падает. Без этого шага "восстановление" могло
#      бы молча ничего не доказывать — например, если бы volume на самом
#      деле пережил `down -v` (баг compose/докера) или скрипт использовал
#      старый volume по ошибке.
#   5. `down -v` снова (чистим доказательный инстанс).
#   6. Restore: файл кладётся в volume ОДНОРАЗОВЫМ alpine-контейнером ДО
#      первого старта сервера, сам сервер поднимается с
#      `REDIS_APPENDONLY=no`.
#
#      Почему REDIS_APPENDONLY=no обязателен для restore, а не опция —
#      проверено живьём, не из документации: с дефолтным
#      REDIS_APPENDONLY=yes (base.yml) сервер на пустом иначе volume с
#      ОДНИМ только dump.rdb полностью его ИГНОРИРУЕТ — создаёт пустой AOF
#      base file и стартует с DBSIZE=0, БЕЗ единой ошибки или
#      предупреждения в логе (`docker logs` показывает "BGSAVE done, 0 keys
#      saved, 0 keys skipped" — сервер создаёт СВОЙ пустой снапшот поверх
#      нашего, а не считает наш). Это ровно тот силовой сценарий "restore
#      молча ничего не восстановил", о котором предупреждает задание — и он
#      воспроизводится без единой ошибки в логе, только по DBSIZE. С
#      REDIS_APPENDONLY=no тот же volume с тем же dump.rdb даёт "Done
#      loading RDB, keys loaded: N" и корректный DBSIZE. Практический вывод
#      для README «Типовые инциденты»: план восстановления "положить
#      dump.rdb в data dir и поднять сервис" молча не работает, если AOF
#      включён конфигом по умолчанию — нужно либо временно выключить
#      appendonly на время загрузки, либо убедиться, что каталога
#      appendonlydir нет ДО первого старта.
#   7. Проверка: DBSIZE после restore == DBSIZE до backup (фатально, если
#      нет) И содержимое точечного ключа (HGETALL) побайтово совпадает с
#      сохранённым до backup (фатально, если нет) — это НЕЗАВИСИМАЯ от
#      DBSIZE проверка: совпадение числа ключей ничего не говорит о том,
#      что в них лежит то же самое.
#   8. `down -v` — финальная уборка.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

IMAGE="${1:?usage: ops-demo.sh <image>, напр. redis:8.8 или valkey/valkey:8.1}"
USERS="${USERS:-300}"
EVENTS_PER_USER="${EVENTS_PER_USER:-20}"
VOLUME="redis-cookbook_redis-master-data"
SPOT_KEY="event:user-00000:0"
RDB_FILE="ops-backup-dump.rdb"

case "$IMAGE" in
  redis:*) LABEL="redis" ;;
  valkey/*) LABEL="valkey" ;;
  *) LABEL="$(echo "$IMAGE" | tr '/:' '__')" ;;
esac

mkdir -p scratchout
LOG="scratchout/ops-demo-${LABEL}.log"
: > "$LOG"
log() { echo "$*" | tee -a "$LOG"; }
fatal() { log "ФАТАЛЬНО: $*"; exit 1; }

rm -f "scratchout/$RDB_FILE"
export REDIS_IMAGE="$IMAGE"

log ""
log "########## backup/restore image=$IMAGE users=$USERS events-per-user=$EVENTS_PER_USER ##########"

# 1. Свежий инстанс, сеем датасет.
docker compose -f compose/base.yml down -v >>"$LOG" 2>&1 || true
docker compose -f compose/base.yml up -d redis-master 2>&1 | tee -a "$LOG"

log "--- сею датасет: $USERS пользователей x $EVENTS_PER_USER событий (dataset/) ---"
(cd dataset && go run . -load -addr 127.0.0.1:6379 -users "$USERS" -events-per-user "$EVENTS_PER_USER") 2>&1 | tee -a "$LOG"

ORIGINAL_SIZE="$(docker exec redis-master redis-cli dbsize)"
log "--- DBSIZE до backup: $ORIGINAL_SIZE ---"
[ "$ORIGINAL_SIZE" -gt 0 ] || fatal "DBSIZE до backup — 0: сев не сработал, backup/restore измерял бы пустоту"

SPOT_BEFORE="$(docker exec redis-master redis-cli hgetall "$SPOT_KEY" | sort)"
log "--- точечный ключ $SPOT_KEY до backup (отсортировано): $(echo "$SPOT_BEFORE" | tr '\n' ' ') ---"
[ -n "$SPOT_BEFORE" ] || fatal "точечный ключ $SPOT_KEY пуст ДО backup — сев не создал ожидаемых данных"

# 2. Живой снапшот через redis-cli --rdb (SYNC) — с сети compose, без
#    docker exec внутрь контейнера. MSYS_NO_PATHCONV — иначе Git Bash на
#    Windows подменяет "/out" на путь Windows-хоста (см. topology-demo.sh).
log "--- redis-cli --rdb: живой снапшот в scratchout/$RDB_FILE ---"
MSYS_NO_PATHCONV=1 docker run --rm --network redis-cookbook-net \
  -v "$HERE/scratchout:/out" "$IMAGE" \
  redis-cli -h redis-master --rdb "/out/$RDB_FILE" 2>&1 | tee -a "$LOG"
[ -s "scratchout/$RDB_FILE" ] || fatal "scratchout/$RDB_FILE не создан или пуст — снапшот не снят"
DUMP_SIZE="$(wc -c < "scratchout/$RDB_FILE" | tr -d ' ')"
log "--- scratchout/$RDB_FILE: $DUMP_SIZE байт ---"

# 3. Уничтожение volume целиком (не просто остановка контейнера).
log "--- down -v: уничтожаю volume $VOLUME ---"
docker compose -f compose/base.yml down -v 2>&1 | tee -a "$LOG"

# 4. Доказательство, что данные РЕАЛЬНО исчезли — тот же путь (down -v ->
#    up), что и restore ниже, но БЕЗ восстановления файла.
log "--- доказательство удаления: свежий пустой инстанс на новом volume ---"
docker compose -f compose/base.yml up -d redis-master 2>&1 | tee -a "$LOG"
EMPTY_SIZE="$(docker exec redis-master redis-cli dbsize)"
log "--- DBSIZE на заведомо пустом инстансе: $EMPTY_SIZE ---"
[ "$EMPTY_SIZE" = "0" ] || fatal "DBSIZE=$EMPTY_SIZE на 'пустом' инстансе — volume не был реально уничтожен, доказательство удаления не прошло"
EXISTS_AFTER_DESTROY="$(docker exec redis-master redis-cli exists "$SPOT_KEY")"
[ "$EXISTS_AFTER_DESTROY" = "0" ] || fatal "точечный ключ $SPOT_KEY пережил down -v — данные не были реально уничтожены"
log "--- подтверждено: DBSIZE=0 и $SPOT_KEY отсутствует — данные ДЕЙСТВИТЕЛЬНО были уничтожены, restore ниже не 'восстанавливает' то, что и так осталось ---"

docker compose -f compose/base.yml down -v 2>&1 | tee -a "$LOG"

# 5. Restore: файл — в volume ДО первого старта сервера; appendonly
#    временно off на время загрузки (см. комментарий в шапке файла).
#
#    Свежесть volume именно ПРОВЕРЯЕТСЯ, а не предполагается: если
#    предыдущий down -v его не удалил, docker volume create молча вернул бы
#    существующий том со старыми данными, и restore "восстановил" бы то, что
#    и так лежало — ровно тот молчаливый успех, который этот стенд обязан
#    ловить.
if docker volume inspect "$VOLUME" >/dev/null 2>&1; then
  fatal "volume $VOLUME существует ДО restore — предыдущий down -v его не удалил, restore нельзя считать доказательным"
fi
log "--- restore: создаю volume $VOLUME, копирую $RDB_FILE в /data/dump.rdb ---"
docker volume create "$VOLUME" >>"$LOG" 2>&1
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$VOLUME:/data" -v "$HERE/scratchout:/backup:ro" \
  alpine:3 sh -c "cp /backup/$RDB_FILE /data/dump.rdb && ls -la /data" 2>&1 | tee -a "$LOG"

log "--- поднимаю redis-master с REDIS_APPENDONLY=no поверх подготовленного volume ---"
REDIS_APPENDONLY=no docker compose -f compose/base.yml up -d redis-master 2>&1 | tee -a "$LOG"
sleep 2
log "--- docker logs redis-master (строки о загрузке) ---"
docker logs redis-master 2>&1 | grep -iE "rdb|loading" | tee -a "$LOG" || true

# 6. Проверки: количество И содержимое, независимо друг от друга.
RESTORED_SIZE="$(docker exec redis-master redis-cli dbsize)"
log "--- DBSIZE после restore: $RESTORED_SIZE (было до backup: $ORIGINAL_SIZE) ---"
[ "$RESTORED_SIZE" = "$ORIGINAL_SIZE" ] || fatal "DBSIZE после restore ($RESTORED_SIZE) != DBSIZE до backup ($ORIGINAL_SIZE) — restore не восстановил тот же датасет"

SPOT_AFTER="$(docker exec redis-master redis-cli hgetall "$SPOT_KEY" | sort)"
log "--- точечный ключ $SPOT_KEY после restore (отсортировано): $(echo "$SPOT_AFTER" | tr '\n' ' ') ---"
[ "$SPOT_AFTER" = "$SPOT_BEFORE" ] || fatal "содержимое $SPOT_KEY после restore не совпадает с содержимым до backup — DBSIZE совпал, но данные другие"
log "--- подтверждено: DBSIZE совпал И содержимое точечного ключа побайтово совпало — restore реально восстановил ИМЕННО этот датасет, а не просто N любых ключей ---"

# 7. Финальная уборка.
docker compose -f compose/base.yml down -v 2>&1 | tee -a "$LOG"
rm -f "scratchout/$RDB_FILE"

log ""
log "[ops-demo] backup/restore готово: $LOG"
