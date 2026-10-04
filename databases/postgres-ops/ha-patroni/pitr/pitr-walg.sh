#!/usr/bin/env bash
# PITR через wal-g: тот же сценарий, другой инструмент. Базовый бэкап (backup-push) +
# архив WAL (wal-push) → восстановление базы (backup-fetch) и накат WAL до момента T1.
# Хранилище — локальный каталог (WALG_FILE_PREFIX), в проде это S3/совместимое.
# Запуск: ./pitr-walg.sh   (после docker compose up -d)
set -uo pipefail
cd "$(dirname "$0")"
X() { docker compose exec -T pg-walg bash -c "export PGDATA=/var/lib/postgresql/data/pgdata WALG_FILE_PREFIX=/var/lib/postgresql/walg PGHOST=/var/run/postgresql PGUSER=postgres PGDATABASE=postgres; $1"; }
BIN=/usr/lib/postgresql/18/bin
PSQL="$BIN/psql -p 5432 -d postgres -tAc"

echo "### initdb + archive_command=wal-g"
X "rm -rf \$PGDATA \$WALG_FILE_PREFIX && mkdir -p /var/lib/postgresql/data \$WALG_FILE_PREFIX && $BIN/initdb -D \$PGDATA -U postgres >/dev/null"
X "cat >> \$PGDATA/postgresql.conf <<CONF
archive_mode = on
archive_command = 'wal-g wal-push %p'
wal_level = replica
listen_addresses = '*'
CONF"
X "$BIN/pg_ctl -D \$PGDATA -l /tmp/pg.log -w start >/dev/null"

echo "### базовый бэкап (wal-g backup-push)"
X "$PSQL \"CREATE TABLE t(id int, note text, ts timestamptz default now())\"" >/dev/null
X "wal-g backup-push \$PGDATA" 2>&1 | grep -iE 'finished|reached' | tail -1

echo "### хорошие данные (до точки восстановления)"
X "$PSQL \"INSERT INTO t(id,note) SELECT g,'good' FROM generate_series(1,1000) g\"" >/dev/null
target=$(X "$PSQL \"SELECT now()\"")
echo "    точка восстановления T1 = $target"
X "$PSQL \"SELECT pg_switch_wal()\"" >/dev/null   # закрыть текущий WAL-сегмент в архив
sleep 2

echo "### ОШИБОЧНЫЕ данные (после T1)"
X "$PSQL \"INSERT INTO t(id,note) SELECT g,'OOPS' FROM generate_series(1,500) g\"" >/dev/null
X "$PSQL \"SELECT pg_switch_wal()\"" >/dev/null
echo "    сейчас в таблице:"
X "$PSQL \"SELECT note, count(*) FROM t GROUP BY note ORDER BY note\"" | sed 's/^/      /'

echo "### восстановление: backup-fetch + накат WAL до T1"
X "$BIN/pg_ctl -D \$PGDATA -m fast -w stop >/dev/null"
X "rm -rf \$PGDATA && wal-g backup-fetch \$PGDATA LATEST" 2>&1 | grep -iE 'finished|fetched' | tail -1
X "cat >> \$PGDATA/postgresql.conf <<CONF
restore_command = 'wal-g wal-fetch %f %p'
recovery_target_time = '$target'
recovery_target_action = 'promote'
CONF"
X "touch \$PGDATA/recovery.signal"
X "$BIN/pg_ctl -D \$PGDATA -l /tmp/pg.log -w start >/dev/null" || true
X "for i in \$(seq 1 30); do [ \"\$($PSQL 'SELECT pg_is_in_recovery()' 2>/dev/null)\" = f ] && break; sleep 1; done"

echo "### после восстановления (ошибочные исчезли, хорошие на месте):"
X "$PSQL \"SELECT note, count(*) FROM t GROUP BY note ORDER BY note\"" | sed 's/^/      /'
