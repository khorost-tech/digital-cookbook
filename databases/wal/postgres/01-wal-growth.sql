DROP TABLE IF EXISTS t;
CREATE TABLE t (id bigserial PRIMARY KEY, v text);
SELECT setseed(0.42);
\echo === LSN до вставки ===
SELECT pg_current_wal_lsn() AS lsn_before \gset
INSERT INTO t (v) SELECT md5(g::text) FROM generate_series(1,1000000) g;
\echo === LSN после вставки 1M строк ===
SELECT pg_current_wal_lsn() AS lsn_after \gset
SELECT pg_size_pretty(pg_wal_lsn_diff(:'lsn_after', :'lsn_before')) AS wal_written_1m_insert;
\echo === апдейт всех строк ===
SELECT pg_current_wal_lsn() AS lsn_u1 \gset
UPDATE t SET v = v || 'x';
SELECT pg_current_wal_lsn() AS lsn_u2 \gset
SELECT pg_size_pretty(pg_wal_lsn_diff(:'lsn_u2', :'lsn_u1')) AS wal_written_1m_update;
\echo === сегменты WAL до и после CHECKPOINT ===
SELECT count(*) AS wal_files_before FROM pg_ls_waldir();
CHECKPOINT;
SELECT count(*) AS wal_files_after FROM pg_ls_waldir();
