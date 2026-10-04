#!/usr/bin/env bash
# Стенд #5: прогоняет матрицу maxmemory-policy (allkeys-lru / allkeys-lfu /
# volatile-ttl / noeviction) x образ (redis:8.8 / valkey/valkey:8.1).
# Каждая ячейка — свежий redis-master (compose/base.yml), поднятый с
# --maxmemory 64mb --maxmemory-policy <policy> (env REDIS_MAXMEMORY /
# REDIS_MAXMEMORY_POLICY). Клиент (memory-eviction/) заливает hot/cold
# ключи, перечитывает hot по кругу (LRU/LFU-сигнал), доливает filler до
# устойчивого вытеснения (или до "OOM command not allowed" на noeviction),
# считает выживших. После каждой ячейки — MEMORY DOCTOR и полный INFO memory
# с хоста (независимый ground truth, не через сам Go-клиент), затем down -v
# (чистый volume перед следующей ячейкой).
#
# Дополнительно (не часть матрицы, отдельная проверка задокументированного
# поведения): volatile-ttl без единого TTL на ключах — по документации Redis
# должен выродиться в noeviction (пустое volatile-множество нечего
# вытеснять). Проверяется живьём отдельным прогоном.
#
# Сырой вывод — per-policy в scratchout/eviction-<образ>-<policy>.log,
# и вся матрица одним файлом в scratchout/memory-eviction-<образ>.log.
#
# Использование:
#   ./ops/eviction-demo.sh redis:8.8
#   ./ops/eviction-demo.sh valkey/valkey:8.1
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

IMAGE="${1:?usage: eviction-demo.sh <image>, напр. redis:8.8 или valkey/valkey:8.1}"
HOT="${HOT:-200}"
COLD="${COLD:-2000}"

case "$IMAGE" in
  redis:*) LABEL="redis" ;;
  valkey/*) LABEL="valkey" ;;
  *) LABEL="$(echo "$IMAGE" | tr '/:' '__')" ;;
esac

mkdir -p scratchout
COMBINED="scratchout/memory-eviction-${LABEL}.log"
: > "$COMBINED"

run_policy() {
  local policy="$1"
  local scenario="$2" # fill-until-oom | volatile-ttl-degenerate
  local log="scratchout/eviction-${LABEL}-${policy}$([ "$scenario" = "volatile-ttl-degenerate" ] && echo "-degenerate").log"
  : > "$log"

  export REDIS_IMAGE="$IMAGE"
  export REDIS_MAXMEMORY="64mb"
  export REDIS_MAXMEMORY_POLICY="$policy"

  {
    echo ""
    echo "########## policy=$policy scenario=$scenario image=$IMAGE maxmemory=64mb ##########"
  } | tee -a "$log" "$COMBINED"

  docker compose -f compose/base.yml up -d redis-master 2>&1 | tee -a "$log" "$COMBINED"

  (cd memory-eviction && REDIS_ADDR=127.0.0.1:6379 go run . \
    -scenario "$scenario" -policy "$policy" -hot "$HOT" -cold "$COLD") 2>&1 | tee -a "$log" "$COMBINED"

  {
    echo ""
    echo "--- MEMORY DOCTOR (с хоста, независимо от Go-клиента) ---"
  } | tee -a "$log" "$COMBINED"
  docker exec redis-master redis-cli memory doctor 2>&1 | tee -a "$log" "$COMBINED"

  {
    echo ""
    echo "--- INFO memory (полный, с хоста) ---"
  } | tee -a "$log" "$COMBINED"
  docker exec redis-master redis-cli info memory 2>&1 | tee -a "$log" "$COMBINED"

  docker compose -f compose/base.yml down -v 2>&1 | tee -a "$log" "$COMBINED"
}

for policy in allkeys-lru allkeys-lfu volatile-ttl noeviction; do
  run_policy "$policy" "fill-until-oom"
done

# Бонус: volatile-ttl без TTL на ключах — проверка вырождения в noeviction.
run_policy "volatile-ttl" "volatile-ttl-degenerate"

echo "" | tee -a "$COMBINED"
echo "[eviction-demo] готово: $COMBINED (и per-policy scratchout/eviction-${LABEL}-*.log)"
