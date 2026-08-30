#!/usr/bin/env bash
set -euo pipefail
# демонстрация AOF в Redis: appendonly yes + appendfsync (аналог synchronous_commit) —
# тот же write-ahead-принцип, масштабированный до in-memory-кэша
R(){ docker compose exec -T redis redis-cli "$@"; }

docker compose up -d
sleep 4

# массовая вставка 1000 ключей ОДНИМ pipe (mass-insertion protocol) — на порядок
# быстрее 1000 отдельных `docker compose exec`; каждая команда в inline-формате с CRLF
echo "=== вставка 1000 ключей (redis-cli --pipe) ==="
for i in $(seq 1 1000); do printf 'SET k:%d v%d\r\n' "$i" "$i"; done \
  | docker compose exec -T redis redis-cli --pipe

echo "=== AOF включён? ==="
R CONFIG GET appendonly
R CONFIG GET appendfsync

echo "=== файл AOF растёт ==="
docker compose exec -T redis sh -c "ls -la /data/appendonlydir/ 2>/dev/null || ls -la /data/ | grep -i aof"

R INFO persistence | grep -iE "aof_enabled|aof_last_write|aof_base_size|aof_current_size"
