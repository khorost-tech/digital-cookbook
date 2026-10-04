#!/usr/bin/env bash
set -euo pipefail
# демонстрация WAL-mode в SQLite: до checkpoint изменения пишутся в test.db-wal,
# а не в основной файл test.db (тот же write-ahead-принцип, что и в PostgreSQL/MySQL/MongoDB)
IMAGE=keinos/sqlite3
export MSYS_NO_PATHCONV=1 # Git Bash on Windows: не переписывать /w в W:/ (no-op на Linux/macOS)

rm -f test.db test.db-wal test.db-shm

# generate_series — опциональное расширение, в keinos/sqlite3 отсутствует;
# используем рекурсивный CTE вместо него
# -i обязателен: без него STDIN не пробрасывается в контейнер и heredoc игнорируется
#
# Важно: sqlite3 CLI при завершении соединения сам делает checkpoint и удаляет
# -wal/-shm, если это последнее соединение к базе. Поэтому файлы -wal/-shm
# смотрим ИЗНУТРИ той же сессии (.shell ls), пока соединение ещё открыто —
# только так виден реальный write-ahead файл до checkpoint.
docker run --rm -i -v "$PWD:/w" -w /w "$IMAGE" sqlite3 test.db <<'SQL'
PRAGMA journal_mode=WAL;
CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT);
WITH RECURSIVE c(x) AS (
  SELECT 1
  UNION ALL
  SELECT x+1 FROM c WHERE x<10000
)
INSERT INTO t(v) SELECT hex(randomblob(16)) FROM c;
PRAGMA journal_mode;
SELECT count(*) FROM t;
.print "=== файлы БД внутри открытого соединения (ожидаем test.db + test.db-wal + test.db-shm) ==="
.shell ls -la /w/test.db*
SQL

echo "=== файлы БД снаружи, после закрытия соединения (sqlite3 CLI сам чекпоинтит на выходе) ==="
ls -la test.db*

echo "=== явный checkpoint новым соединением (на пустом -wal, т.к. предыдущее соединение уже его слило) ==="
docker run --rm -v "$PWD:/w" -w /w "$IMAGE" sqlite3 test.db "PRAGMA wal_checkpoint(TRUNCATE); SELECT count(*) FROM t;"

echo "=== файлы БД после checkpoint ==="
ls -la test.db*
