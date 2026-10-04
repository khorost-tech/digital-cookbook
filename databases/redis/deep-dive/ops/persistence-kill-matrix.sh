#!/usr/bin/env bash
# Стенд #3: прогоняет полную матрицу durability-loss (RDB-only / AOF-always /
# AOF-everysec / AOF-no / hybrid) x (redis:8.8 / valkey/valkey:8.1),
# по каждой ячейке: docker compose up -d redis-master (режим — через env),
# durability-loss (пишет N=5000, SIGKILL на середине), рестарт того же
# контейнера на том же volume, count-recovered (DBSIZE + INFO persistence),
# down -v (чистый volume перед следующей ячейкой). Сырой вывод — tee в
# scratchout/persistence-<образ>.log (не в git, воспроизводимо).
#
# Использование:
#   ./ops/persistence-kill-matrix.sh redis:8.8
#   ./ops/persistence-kill-matrix.sh valkey/valkey:8.1
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

IMAGE="${1:?usage: persistence-kill-matrix.sh <image>, напр. redis:8.8 или valkey/valkey:8.1}"
N="${N:-5000}"

case "$IMAGE" in
  redis:*) LABEL="redis" ;;
  valkey/*) LABEL="valkey" ;;
  *) LABEL="$(echo "$IMAGE" | tr '/:' '__')" ;;
esac

LOG="scratchout/persistence-${LABEL}.log"
mkdir -p scratchout
echo "[persistence-kill-matrix] образ=$IMAGE n=$N лог=$LOG" | tee -a "$LOG"

# set_mode_env заполняет REDIS_APPENDONLY/REDIS_APPENDFSYNC/REDIS_SAVE_SECONDS/
# REDIS_SAVE_CHANGES под конкретный durability-режим (см. компенты в
# compose/base.yml). AOF-режимы получают заведомо большой save-интервал
# (3600с/1000000 изменений), чтобы RDB-снапшот не мог случайно
# подстраховать результат внутри короткого теста — так каждый AOF-режим
# проверяется в изоляции.
set_mode_env() {
  case "$1" in
    rdb-only)
      REDIS_APPENDONLY=no; REDIS_APPENDFSYNC=everysec
      REDIS_SAVE_SECONDS=60; REDIS_SAVE_CHANGES=1
      ;;
    aof-always)
      REDIS_APPENDONLY=yes; REDIS_APPENDFSYNC=always
      REDIS_SAVE_SECONDS=3600; REDIS_SAVE_CHANGES=1000000
      ;;
    aof-everysec)
      REDIS_APPENDONLY=yes; REDIS_APPENDFSYNC=everysec
      REDIS_SAVE_SECONDS=3600; REDIS_SAVE_CHANGES=1000000
      ;;
    aof-no)
      REDIS_APPENDONLY=yes; REDIS_APPENDFSYNC=no
      REDIS_SAVE_SECONDS=3600; REDIS_SAVE_CHANGES=1000000
      ;;
    hybrid)
      # дефолты base.yml без переопределений: save 60 1, appendonly yes, appendfsync everysec
      REDIS_APPENDONLY=yes; REDIS_APPENDFSYNC=everysec
      REDIS_SAVE_SECONDS=60; REDIS_SAVE_CHANGES=1
      ;;
    *)
      echo "неизвестный режим: $1" >&2; exit 1 ;;
  esac
  export REDIS_APPENDONLY REDIS_APPENDFSYNC REDIS_SAVE_SECONDS REDIS_SAVE_CHANGES
}

run_mode() {
  local mode="$1"
  set_mode_env "$mode"
  export REDIS_IMAGE="$IMAGE"

  echo "" | tee -a "$LOG"
  echo "########## mode=$mode image=$IMAGE (appendonly=$REDIS_APPENDONLY appendfsync=$REDIS_APPENDFSYNC save=${REDIS_SAVE_SECONDS} ${REDIS_SAVE_CHANGES}) ##########" | tee -a "$LOG"

  # 1. Поднять redis-master в заданном режиме персистентности (env -> command в base.yml).
  docker compose -f compose/base.yml up -d redis-master 2>&1 | tee -a "$LOG"

  # 2. Записать N ключей, SIGKILL на середине.
  (cd persistence && REDIS_ADDR=127.0.0.1:6379 go run . -scenario durability-loss -n "$N" \
    -container redis-master -mode "$mode") 2>&1 | tee -a "$LOG"

  local confirmed
  confirmed="$(grep -oE 'last confirmed write index: [0-9]+' "$LOG" | tail -1 | grep -oE '[0-9]+$')"

  # 3. Рестарт того же контейнера на том же volume (без down -v между килом и рестартом,
  #    те же env — docker compose узнаёт неизменную конфигурацию и просто стартует
  #    существующий контейнер, а не пересоздаёт его).
  docker compose -f compose/base.yml up -d redis-master 2>&1 | tee -a "$LOG"

  # 4. Посчитать восстановленное число ключей.
  (cd persistence && REDIS_ADDR=127.0.0.1:6379 go run . -scenario count-recovered \
    -container redis-master -mode "$mode" -confirmed "$confirmed") 2>&1 | tee -a "$LOG"

  # 5. Чистый volume перед следующей ячейкой матрицы.
  docker compose -f compose/base.yml down -v 2>&1 | tee -a "$LOG"
}

for mode in rdb-only aof-always aof-everysec aof-no hybrid; do
  run_mode "$mode"
done

echo "[persistence-kill-matrix] готово: $LOG" | tee -a "$LOG"
