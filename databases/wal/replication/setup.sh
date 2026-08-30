#!/usr/bin/env bash
set -euo pipefail

PRI(){ docker compose exec -T primary psql -U postgres -d waldemo -qtAc "$1"; }
STB(){ docker compose exec -T standby psql -U postgres -d waldemo -qtAc "$1"; }

docker compose up -d

echo "=== ждём, пока standby поднимется и догонит primary (pg_basebackup + streaming) ==="
for i in $(seq 1 30); do
  if STB "SELECT 1;" >/dev/null 2>&1; then
    echo "standby отвечает (попытка $i)"
    break
  fi
  echo "standby ещё не готов (попытка $i)..."
  sleep 3
done

PRI "CREATE TABLE IF NOT EXISTS repl(id int); INSERT INTO repl VALUES (42);"
sleep 3

echo "primary repl count: $(PRI 'SELECT count(*) FROM repl;')"
echo "standby repl count: $(STB 'SELECT count(*) FROM repl;')"

echo "=== pg_stat_replication на primary (walreceiver подключён?) ==="
PRI "SELECT application_name, state, sync_state, replay_lag FROM pg_stat_replication;"

echo "=== standby в recovery? ==="
STB "SELECT pg_is_in_recovery();"
