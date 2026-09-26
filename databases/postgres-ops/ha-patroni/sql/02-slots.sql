-- Replication slots на лидере. Slot гарантирует, что лидер не удалит WAL, пока реплика
-- его не забрала, — реплика не отстанет безнадёжно. Обратная сторона: если реплика
-- ОТВАЛИЛАСЬ, а slot остался, restart_lsn застывает и WAL копится, грозя переполнить
-- диск лидера. Выполнять на лидере (:5000).

\echo === слоты репликации и удерживаемый ими WAL ===
SELECT slot_name,
       slot_type,
       active,
       active_pid,
       pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained_wal
FROM pg_replication_slots
ORDER BY slot_name;

\echo === что было бы опасно: неактивный слот с растущим retained_wal ===
-- active=false и большой retained_wal — сигнал, что реплика отвалилась, а WAL копится.
SELECT count(*) FILTER (WHERE NOT active) AS inactive_slots
FROM pg_replication_slots;
