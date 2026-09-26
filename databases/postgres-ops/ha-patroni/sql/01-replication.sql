-- Состояние streaming-репликации со стороны лидера. Выполнять на лидере (:5000).
-- sync_state показывает, ждёт ли лидер подтверждения от реплики (sync) или нет (async).
-- *_lag — задержка на запись WAL / сброс на диск / применение на реплике.

\echo === кто я: лидер или реплика ===
SELECT pg_is_in_recovery() AS is_replica, inet_server_addr() AS host;

\echo === реплики, их состояние и лаг (со стороны лидера) ===
SELECT application_name,
       client_addr,
       state,
       sync_state,
       write_lag,
       flush_lag,
       replay_lag
FROM pg_stat_replication
ORDER BY application_name;

\echo === режим синхронной репликации в конфиге ===
SHOW synchronous_standby_names;
SHOW synchronous_commit;
