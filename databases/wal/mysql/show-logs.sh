#!/usr/bin/env bash
set -euo pipefail
MYSQL(){ docker compose exec -T mysql mysql -uroot -pwaldemo waldemo -e "$1" 2>/dev/null; }
docker compose up -d && sleep 20
MYSQL "CREATE TABLE IF NOT EXISTS t(id INT PRIMARY KEY AUTO_INCREMENT, v VARCHAR(64));"
MYSQL "INSERT INTO t(v) SELECT MD5(RAND()) FROM information_schema.columns LIMIT 500;"
echo "=== binary log (репликация/CDC) ==="
MYSQL "SHOW BINARY LOGS;"
echo "=== redo log (crash recovery) — файлы на диске ==="
docker compose exec -T mysql sh -c "ls -la '/var/lib/mysql/#innodb_redo/' 2>/dev/null | head; ls -la /var/lib/mysql/ | grep -i 'binlog\|ib_' | head"
echo "=== одно логическое изменение → две записи (redo + binlog) ==="
MYSQL "SHOW VARIABLES LIKE 'innodb_redo_log_capacity'; SHOW VARIABLES LIKE 'log_bin';"
