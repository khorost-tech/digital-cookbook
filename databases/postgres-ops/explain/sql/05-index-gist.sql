-- GiST в плане. GiST — сбалансированное дерево для «перекрытий» и близости:
-- диапазоны, геометрия, полнотекст-ранжирование, kNN. В db-indexes GiST детально не
-- разбирался — здесь смотрим, как его узел выглядит в выводе EXPLAIN.

\echo === GiST range: пересечение периодов акций (оператор &&) ===
CREATE INDEX IF NOT EXISTS idx_promotions_period_gist ON promotions USING gist(period);
ANALYZE promotions;
-- Ищем акции, активные в ближайшие сутки. && — «пересекается с».
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM promotions
WHERE period && tstzrange(now(), now() + interval '1 day');

\echo
\echo === GiST kNN: ближайшие по цене товары (оператор <->) ===
-- btree_gist добавляет distance-оператор <-> для скаляров (для numeric он не
-- определён, поэтому индексируем и сравниваем как float8).
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE INDEX IF NOT EXISTS idx_products_price_gist ON products USING gist ((price::float8));
ANALYZE products;
-- ORDER BY ... <-> ... — Index Scan выдаёт строки уже по возрастанию расстояния,
-- узла Sort нет: индекс сам обходит дерево от ближайшего.
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, price FROM products
ORDER BY (price::float8) <-> 500.0 LIMIT 5;
