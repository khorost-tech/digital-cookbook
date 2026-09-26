#!/usr/bin/env bash
# PITR через pgBackRest: базовый бэкап + WAL archiving → восстановление на момент времени.
# Сценарий: залили «хорошие» данные (точка T1), потом «ошибочные» (после T1), затем
# откатили базу ровно на T1 — ошибочные исчезли, хорошие остались.
# Запуск: ./pitr-pgbackrest.sh   (после docker compose up -d)
set -uo pipefail
cd "$(dirname "$0")"
X() { docker compose exec -T pg-pgbackrest bash -c "export PGDATA=/var/lib/postgresql/data/pgdata; $1"; }
BIN=/usr/lib/postgresql/18/bin
PSQL="$BIN/psql -p 5432 -d postgres -tAc"

echo "### initdb + archive_command=pgbackrest"
X "rm -rf \$PGDATA && $BIN/initdb -D \$PGDATA -U postgres >/dev/null"
X "cat >> \$PGDATA/postgresql.conf <<CONF
archive_mode = on
archive_command = 'pgbackrest --stanza=demo archive-push %p'
wal_level = replica
max_wal_senders = 3
listen_addresses = '*'
CONF"
X "$BIN/pg_ctl -D \$PGDATA -l /tmp/pg.log -w start >/dev/null"

echo "### stanza-create + базовый бэкап"
X "pgbackrest --stanza=demo stanza-create" 2>&1 | tail -1
X "$PSQL \"CREATE TABLE t(id int, note text, ts timestamptz default now())\"" >/dev/null
X "pgbackrest --stanza=demo --type=full backup" 2>&1 | grep -iE 'backup command end|new backup' | tail -1

echo "### хорошие данные (до точки восстановления)"
X "$PSQL \"INSERT INTO t(id,note) SELECT g,'good' FROM generate_series(1,1000) g\"" >/dev/null
target=$(X "$PSQL \"SELECT now()\"")
echo "    точка восстановления T1 = $target"
sleep 2

echo "### ОШИБОЧНЫЕ данные (после T1 — их и откатим)"
X "$PSQL \"INSERT INTO t(id,note) SELECT g,'OOPS' FROM generate_series(1,500) g\"" >/dev/null
echo "    сейчас в таблице:"
X "$PSQL \"SELECT note, count(*) FROM t GROUP BY note ORDER BY note\"" | sed 's/^/      /'

echo "### restore --type=time на T1 (откат ошибочных)"
X "$BIN/pg_ctl -D \$PGDATA -m fast -w stop >/dev/null"
X "pgbackrest --stanza=demo --type=time --target=\"$target\" --delta restore" 2>&1 | grep -iE 'restore command end' | tail -1
X "$BIN/pg_ctl -D \$PGDATA -l /tmp/pg.log -w start >/dev/null"
# дождаться выхода из recovery
X "for i in \$(seq 1 30); do [ \"\$($PSQL 'SELECT pg_is_in_recovery()')\" = f ] && break; sleep 1; done"

echo "### после восстановления (ошибочные исчезли, хорошие на месте):"
X "$PSQL \"SELECT note, count(*) FROM t GROUP BY note ORDER BY note\"" | sed 's/^/      /'
