#!/bin/bash
# Реплика: при пустом PGDATA делает pg_basebackup с primary (-R пишет standby.signal
# и primary_conninfo автоматически), затем передаёт управление штатному entrypoint'у образа.
set -euo pipefail

export PGPASSWORD="repl_pw"

if [ -z "$(ls -A "$PGDATA" 2>/dev/null)" ]; then
    echo "PGDATA пуст — выполняю pg_basebackup с pg-primary..."
    until pg_basebackup -h pg-primary -p 5432 -U replicator -D "$PGDATA" -Fp -Xs -R -v -w; do
        echo "pg-primary ещё не готов, повтор через 2с..."
        sleep 2
    done
    chmod 0700 "$PGDATA"
fi

exec docker-entrypoint.sh postgres
