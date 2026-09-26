-- Почему соединение к PostgreSQL — дорогой ресурс. Каждое клиентское соединение
-- обслуживается ОТДЕЛЬНЫМ процессом-бэкендом со своей памятью. Их число ограничено
-- max_connections, и переключение контекста между сотнями процессов съедает CPU
-- задолго до исчерпания полезной работы. Пулер ставит перед базой узкое горлышко.

\echo === соединение = процесс: клиентские бэкенды в pg_stat_activity ===
SELECT pid, backend_type, state, application_name
FROM pg_stat_activity
WHERE backend_type = 'client backend'
ORDER BY pid;

\echo === жёсткий предел прямых соединений ===
SHOW max_connections;

\echo === сколько занято сейчас и сколько всего ===
SELECT count(*) FILTER (WHERE backend_type = 'client backend') AS client_backends,
       count(*)                                                AS all_backends,
       current_setting('max_connections')::int                AS max_conn
FROM pg_stat_activity;

\echo === память на бэкенд (примерная оценка через work_mem/шареды) ===
SHOW work_mem;
SHOW shared_buffers;
