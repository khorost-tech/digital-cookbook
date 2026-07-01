#!/bin/bash
# Инициализация primary: пользователь для репликации, таблица users, доступ реплике по подсети.
# Монтируется в /docker-entrypoint-initdb.d/ — выполняется один раз при первой инициализации PGDATA.
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-SQL
    CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD 'repl_pw';

    CREATE TABLE users (
        id serial PRIMARY KEY,
        name text,
        updated_at timestamptz DEFAULT now()
    );

    INSERT INTO users (name) VALUES ('Alice'), ('Bob');
SQL

# wal_level/max_wal_senders/wal_keep_size задаются через command в docker-compose (-c),
# здесь донастраиваем pg_hba.conf: разрешаем репликацию и обычные подключения из подсети стенда.
{
    echo "host replication replicator 172.30.0.0/16 md5"
    echo "host all           app         172.30.0.0/16 scram-sha-256"
} >> "$PGDATA/pg_hba.conf"
