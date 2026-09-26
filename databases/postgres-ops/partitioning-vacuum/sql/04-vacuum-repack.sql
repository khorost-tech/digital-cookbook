-- Три способа борьбы с bloat и их цена.
--   VACUUM       — убирает мёртвых, место НЕ отдаёт ОС (см. сц.02). Не блокирует.
--   VACUUM FULL  — переписывает таблицу с нуля, отдаёт место ОС. Держит ACCESS EXCLUSIVE:
--                  на время работы таблица недоступна даже для чтения. В проде — простой.
--   pg_repack    — тоже переписывает и отдаёт место, но онлайн: короткая блокировка лишь
--                  в начале и в конце, остальное время читатели и писатели работают.
-- Здесь SQL-часть (VACUUM FULL); pg_repack — CLI-утилита, вызывается отдельно (см. fixture/README).

\timing on

-- ── таблица A: демонстрация VACUUM FULL ──────────────────────────────────
DROP TABLE IF EXISTS bloat_a;
CREATE TABLE bloat_a (id bigint PRIMARY KEY, payload text) WITH (autovacuum_enabled = false);
INSERT INTO bloat_a SELECT g, repeat('x', 100) FROM generate_series(1, 1000000) g;
UPDATE bloat_a SET payload = payload || 'y';   -- раздуваем вдвое
VACUUM bloat_a;                                -- убрали мёртвых, но файл раздут

\echo === bloat_a ДО VACUUM FULL (место занято, хоть мёртвых и нет) ===
SELECT pg_size_pretty(pg_relation_size('bloat_a')) AS heap_size,
       round(free_percent::numeric, 1) AS free_pct
FROM pgstattuple('bloat_a');

\echo === VACUUM FULL: держит ACCESS EXCLUSIVE, зато отдаёт место ОС ===
VACUUM FULL bloat_a;
SELECT pg_size_pretty(pg_relation_size('bloat_a')) AS heap_size_after,
       round(free_percent::numeric, 1) AS free_pct
FROM pgstattuple('bloat_a');

-- ── таблица B: подготовка к pg_repack (раздуть и ОСТАВИТЬ раздутой) ──────
-- pg_repack запускается снаружи как CLI. Здесь только готовим жертву и мерим ДО.
DROP TABLE IF EXISTS bloat_b;
CREATE TABLE bloat_b (id bigint PRIMARY KEY, payload text) WITH (autovacuum_enabled = false);
INSERT INTO bloat_b SELECT g, repeat('x', 100) FROM generate_series(1, 1000000) g;
UPDATE bloat_b SET payload = payload || 'y';
VACUUM bloat_b;

\echo === bloat_b ДО pg_repack (раздута; repack запустить CLI-командой из fixture) ===
SELECT pg_size_pretty(pg_relation_size('bloat_b')) AS heap_size,
       round(free_percent::numeric, 1) AS free_pct
FROM pgstattuple('bloat_b');
