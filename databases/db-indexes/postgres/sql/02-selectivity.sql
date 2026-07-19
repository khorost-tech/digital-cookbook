CREATE INDEX IF NOT EXISTS idx_events_amount ON events(amount);
ANALYZE events;
-- amount ∈ [0,1000]; порог t даёт долю ≈ t/1000
-- count(payload) вместо count(*): payload не входит в idx_events_amount,
-- поэтому планировщик обязан читать heap — это форсирует реальный
-- Index Scan / Bitmap Heap Scan / Seq Scan кроссовер вместо Index Only Scan на всех селективностях.
\echo === селективность ~0.05% (amount < 0.5) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 0.5;
\echo === ~0.5% (amount < 5) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 5;
\echo === ~2% (amount < 20) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 20;
\echo === ~10% (amount < 100) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 100;
\echo === ~30% (amount < 300) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 300;
\echo === ~50% (amount < 500) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 500;
\echo === ~70% (amount < 700) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 700;
\echo === ~90% (amount < 900) ===
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE amount < 900;
