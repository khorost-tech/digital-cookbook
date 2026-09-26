-- GiN в плане. GiN — инвертированный индекс для составных значений (текст, jsonb, массивы).
-- Ключевое наблюдение в выводе: GiN даёт Bitmap Index Scan, а над Bitmap Heap Scan
-- появляется Recheck Cond — GiN хранит не сами значения, а лексемы/ключи, поэтому
-- совпадение по индексу перепроверяется на строке.

\echo === GiN полнотекст (tsvector @@ tsquery) ===
CREATE INDEX IF NOT EXISTS idx_products_search_gin ON products USING gin(search);
ANALYZE products;
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, title FROM products
WHERE search @@ to_tsquery('russian', 'ноутбук & игровой');

\echo
\echo === GiN jsonb containment (@>) — тот же тип узла, другой оператор ===
CREATE INDEX IF NOT EXISTS idx_products_attrs_gin ON products USING gin(attrs);
ANALYZE products;
-- Более селективный ключ, чтобы индекс выиграл у seq scan.
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM products
WHERE attrs @> '{"brand":"acme","color":"blue"}';

\echo
\echo === что именно лежит в индексе (для понимания Recheck) ===
-- GiN разбирает jsonb/tsvector на ключи; строка перепроверяется, отсюда Recheck Cond.
SELECT title, search FROM products
WHERE search @@ to_tsquery('russian', 'ноутбук & игровой') LIMIT 1;
