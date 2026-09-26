-- Небольшая таблица для демонстраций (наблюдение соединений, поломки transaction pooling).
-- Нагрузочные таблицы pgbench создаёт отдельно (pgbench -i, см. bench.sh).

DROP TABLE IF EXISTS demo;
CREATE TABLE demo (id int PRIMARY KEY, val text);
INSERT INTO demo SELECT g, 'row' || g FROM generate_series(1, 1000) g;

SELECT count(*) AS demo_rows FROM demo;
