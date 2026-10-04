-- GiN в плане. GiN — инвертированный индекс для составных значений (текст, jsonb, массивы).
-- Ключевое наблюдение в выводе: GiN даёт Bitmap Index Scan под Bitmap Heap Scan.
-- Строка Recheck Cond печатается у любого Bitmap Heap Scan: это условие, которое
-- перепроверяется по строке, ЕСЛИ понадобится — битовая карта стала lossy (не
-- влезла в work_mem) или сам класс операторов не отвечает точно. Для tsvector
-- такое бывает у запросов с весами (веса в индексе не хранятся); обычный & без
-- весов GiN отвечает точно. Стемминг к recheck отношения не имеет.

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
\echo === что именно лежит в индексе ===
-- GiN разбирает jsonb/tsvector на ключи: в индексе лексема 'игров', а не слово 'игровой'.
SELECT title, search FROM products
WHERE search @@ to_tsquery('russian', 'ноутбук & игровой') LIMIT 1;
