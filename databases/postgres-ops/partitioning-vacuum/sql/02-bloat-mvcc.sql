-- Bloat из-за MVCC. PostgreSQL при UPDATE не меняет строку на месте, а создаёт новую
-- версию и помечает старую мёртвой. DELETE тоже лишь помечает. Мёртвые версии копятся,
-- пока их не уберёт VACUUM — но VACUUM возвращает место лишь для ПЕРЕИСПОЛЬЗОВАНИЯ,
-- не отдавая его операционной системе. Меряем это pgstattuple, а не на глаз.
-- autovacuum на таблице отключён, чтобы наблюдать эффект в чистом виде.

DROP TABLE IF EXISTS bloat_demo;
CREATE TABLE bloat_demo (id bigint PRIMARY KEY, payload text)
    WITH (autovacuum_enabled = false, fillfactor = 100);

INSERT INTO bloat_demo SELECT g, repeat('x', 100) FROM generate_series(1, 500000) g;
VACUUM bloat_demo;   -- чистое исходное состояние

\echo === исходно: мёртвых версий почти нет ===
SELECT pg_size_pretty(pg_relation_size('bloat_demo')) AS heap_size,
       tuple_count, dead_tuple_count,
       round(dead_tuple_percent::numeric, 2) AS dead_pct,
       round(free_percent::numeric, 2)       AS free_pct
FROM pgstattuple('bloat_demo');

\echo === массовый UPDATE всех строк → 500k мёртвых версий ===
UPDATE bloat_demo SET payload = payload || 'y';   -- каждая строка получает новую версию
SELECT n_live_tup, n_dead_tup
FROM pg_stat_user_tables WHERE relname = 'bloat_demo';

\echo --- размер вырос, dead_tuple_percent высок: это и есть bloat ---
SELECT pg_size_pretty(pg_relation_size('bloat_demo')) AS heap_size,
       dead_tuple_count,
       round(dead_tuple_percent::numeric, 2) AS dead_pct
FROM pgstattuple('bloat_demo');

\echo === обычный VACUUM: убирает мёртвых, но НЕ отдаёт место ОС ===
VACUUM bloat_demo;
\echo --- размер файла ТОТ ЖЕ (не уменьшился!), но теперь это free space ---
SELECT pg_size_pretty(pg_relation_size('bloat_demo')) AS heap_size,
       dead_tuple_count,
       round(free_percent::numeric, 2) AS free_pct
FROM pgstattuple('bloat_demo');

\echo === доказательство переиспользования: новые строки идут в free space ===
-- Файл не растёт, пока новые строки помещаются в освобождённое VACUUM место.
INSERT INTO bloat_demo SELECT g, repeat('z', 100) FROM generate_series(500001, 700000) g;
SELECT pg_size_pretty(pg_relation_size('bloat_demo')) AS heap_size_after_insert;
