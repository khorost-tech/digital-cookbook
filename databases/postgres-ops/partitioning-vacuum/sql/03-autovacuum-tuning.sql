-- Тюнинг autovacuum. Autovacuum запускает VACUUM по таблице, когда число мёртвых
-- версий превысит порог:  threshold + scale_factor * n_live_tuples.
-- Дефолтный scale_factor=0.2 означает «ждать, пока протухнет 20% таблицы» — для
-- больших и горячих таблиц это слишком поздно. Лечится per-table настройкой.
-- naptime на стенде снижен до 10s (см. docker-compose), чтобы не ждать минуту.

DROP TABLE IF EXISTS av_demo;
CREATE TABLE av_demo (id bigint PRIMARY KEY, payload text);
INSERT INTO av_demo SELECT g, repeat('x', 100) FROM generate_series(1, 500000) g;
VACUUM ANALYZE av_demo;   -- reltuples=500000, чистый старт

\echo === дефолтные пороги и вычисленный порог для этой таблицы ===
SELECT current_setting('autovacuum_vacuum_threshold')     AS threshold,
       current_setting('autovacuum_vacuum_scale_factor')  AS scale_factor;
-- порог = 50 + 0.2 * 500000 = 100050 мёртвых версий

\echo === UPDATE 60k строк (60000 dead < 100050 порог) ===
UPDATE av_demo SET payload = payload || 'y' WHERE id <= 60000;
SELECT pg_sleep(15);   -- больше naptime: даём autovacuum шанс проснуться

\echo --- autovacuum НЕ сработал: порог не достигнут, мёртвые версии копятся ---
SELECT n_dead_tup, autovacuum_count,
       coalesce(last_autovacuum::text, '(не было)') AS last_autovacuum
FROM pg_stat_user_tables WHERE relname = 'av_demo';

\echo === тюнинг: снижаем порог для этой конкретной таблицы ===
ALTER TABLE av_demo SET (autovacuum_vacuum_scale_factor = 0.01,
                         autovacuum_vacuum_threshold    = 1000);
-- новый порог = 1000 + 0.01 * 500000 = 6000; уже накопленные 60000 >> 6000
SELECT pg_sleep(15);   -- ждём следующий проход autovacuum

\echo --- теперь autovacuum догнал: мёртвые убраны, счётчик вырос ---
SELECT n_dead_tup, autovacuum_count,
       coalesce(last_autovacuum::text, '(не было)') AS last_autovacuum
FROM pg_stat_user_tables WHERE relname = 'av_demo';
