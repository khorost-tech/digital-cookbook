-- btree в плане: самый частый индекс. Смотрим, как он проявляется в выводе.
-- Ключевое наблюдение — btree хранит значения ОТСОРТИРОВАННО, поэтому даёт не только
-- поиск (Index Cond), но и бесплатный порядок (нет узла Sort для ORDER BY).

CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
ANALYZE orders;

\echo === Равенство: Index Cond по btree ===
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders WHERE created_at = (SELECT max(created_at) FROM orders);

\echo
\echo === ORDER BY + LIMIT: индекс даёт порядок, узла Sort НЕТ ===
-- Сравните: без индекса тут был бы Sort (или Top-N heapsort) над всей таблицей.
-- С btree план просто идёт по индексу с конца и берёт 10 строк.
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders ORDER BY created_at DESC LIMIT 10;

\echo
\echo === Range: Index Cond с двумя границами ===
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders
WHERE created_at BETWEEN now() - interval '7 days' AND now();

\echo
\echo === Контрдемонстрация: тот же ORDER BY, но индекс временно отключён ===
-- Показывает узел Sort, который btree убрал выше.
SET enable_indexscan = off;
SET enable_bitmapscan = off;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders ORDER BY created_at DESC LIMIT 10;
RESET enable_indexscan;
RESET enable_bitmapscan;
