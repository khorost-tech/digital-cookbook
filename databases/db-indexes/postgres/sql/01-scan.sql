\echo === (1) равенство по user_id БЕЗ индекса ===
EXPLAIN (ANALYZE, BUFFERS, COSTS) SELECT * FROM events WHERE user_id = 12345;

\echo === (2) тот же запрос ПОСЛЕ CREATE INDEX ===
CREATE INDEX idx_events_user ON events(user_id);
ANALYZE events;
EXPLAIN (ANALYZE, BUFFERS, COSTS) SELECT * FROM events WHERE user_id = 12345;

\echo === (3) размеры heap и индекса ===
SELECT pg_size_pretty(pg_relation_size('events'))        AS heap,
       pg_size_pretty(pg_relation_size('idx_events_user')) AS idx;

\echo === (4a) covering: ОДИНАКОВАЯ проекция user_id, amount БЕЗ covering-индекса ===
-- Честное сравнение: та же проекция, что и в (4b). Пока covering-индекса нет,
-- обычный idx_events_user(user_id) даёт Index Scan и идёт в heap за amount.
EXPLAIN (ANALYZE, BUFFERS) SELECT user_id, amount FROM events WHERE user_id = 12345;

\echo === (4b) covering-индекс + VACUUM примирует visibility map ===
CREATE INDEX idx_events_user_cov ON events(user_id) INCLUDE (amount);
-- VACUUM (не ANALYZE!) выставляет visibility map в all-visible для не менявшихся
-- страниц. Без него Index Only Scan вынужден идти в heap за проверкой видимости
-- каждой строки (виден как Heap Fetches: N > 0). Именно VM, а не наличие
-- covering-индекса, определяет, будет ли scan действительно «only».
VACUUM (ANALYZE) events;
-- Та же проекция user_id, amount: теперь Index Only Scan, Heap Fetches: 0.
EXPLAIN (ANALYZE, BUFFERS) SELECT user_id, amount FROM events WHERE user_id = 12345;
