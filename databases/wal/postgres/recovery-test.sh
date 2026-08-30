#!/usr/bin/env bash
set -euo pipefail
PSQL(){ docker compose exec -T postgres psql -U postgres -d waldemo -qtAc "$1"; }
docker compose up -d && sleep 8
PSQL "DROP TABLE IF EXISTS crashtest; CREATE TABLE crashtest(id int);"
# вставляем и КОММИТИМ 1000 строк (synchronous_commit=on → WAL на диске)
PSQL "SET synchronous_commit=on; INSERT INTO crashtest SELECT generate_series(1,1000);"
echo "до падения: $(PSQL 'SELECT count(*) FROM crashtest;')"
echo "=== SIGKILL контейнера (жёсткое падение) ==="
docker compose kill -s SIGKILL postgres
sleep 2
echo "=== рестарт ==="
docker compose up -d && sleep 10
echo "после восстановления: $(PSQL 'SELECT count(*) FROM crashtest;')"
echo "=== строки лога про recovery ==="
docker compose logs postgres 2>&1 | grep -iE "recover|not properly shut down|redo|starting" | tail -5
