-- BRIN в плане. BRIN хранит не значения, а мин/макс на диапазон блоков (по умолчанию 128).
-- Крошечный размер ценой грубости: в плане это Bitmap Index Scan с Recheck по блокам.
-- Работает только когда данные ФИЗИЧЕСКИ упорядочены по колонке (высокая correlation).
-- Тут orders.created_at растёт вместе с id — идеальный случай.

\echo === correlation created_at (должна быть ~1: append-only по времени) ===
SELECT correlation FROM pg_stats WHERE tablename='orders' AND attname='created_at';

\echo
\echo === чтобы увидеть BRIN в плане, временно убираем btree по той же колонке ===
-- Иначе планировщик берёт более точный btree (idx_orders_created из сц.03).
DROP INDEX IF EXISTS idx_orders_created;
CREATE INDEX IF NOT EXISTS idx_orders_created_brin ON orders USING brin(created_at);
ANALYZE orders;

\echo === BRIN: Bitmap Index Scan, Recheck Cond по диапазонам блоков ===
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders
WHERE created_at BETWEEN now() - interval '7 days' AND now();

\echo
\echo === возвращаем btree и сравниваем размеры BRIN vs btree ===
CREATE INDEX idx_orders_created ON orders(created_at);
SELECT indexrelname,
       pg_size_pretty(pg_relation_size(indexrelid)) AS size,
       pg_relation_size(indexrelid) AS bytes
FROM pg_stat_user_indexes
WHERE relname='orders' AND indexrelname LIKE '%created%'
ORDER BY pg_relation_size(indexrelid);
