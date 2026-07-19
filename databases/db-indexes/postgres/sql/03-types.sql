\echo === hash: равенство ===
CREATE INDEX idx_status_hash ON events USING hash(status);
ANALYZE events;
EXPLAIN (ANALYZE) SELECT count(*) FROM events WHERE status = 'refunded';

\echo === GIN по jsonb (payload->>'tag') ===
CREATE INDEX idx_payload_gin ON events USING gin(payload);
ANALYZE events;
EXPLAIN (ANALYZE) SELECT count(*) FROM events WHERE payload @> '{"tag":"a"}';

\echo === BRIN на created_at — БЕСПОЛЕЗЕН без физической корреляции ===
CREATE INDEX idx_created_brin ON events USING brin(created_at);
ANALYZE events;
SELECT correlation FROM pg_stats WHERE tablename='events' AND attname='created_at';
EXPLAIN (ANALYZE) SELECT count(*) FROM events WHERE created_at > now() - interval '7 days';

\echo === BRIN на id (физически упорядочен) — РАБОТАЕТ ===
CREATE INDEX idx_id_brin ON events USING brin(id);
ANALYZE events;
SELECT correlation FROM pg_stats WHERE tablename='events' AND attname='id';
EXPLAIN (ANALYZE) SELECT count(payload) FROM events WHERE id BETWEEN 1500000 AND 1510000;

\echo === partial: только paid ===
CREATE INDEX idx_paid_partial ON events(user_id) WHERE status = 'paid';
ANALYZE events;
EXPLAIN (ANALYZE) SELECT * FROM events WHERE status='paid' AND user_id=777;

\echo === expression: lower(status) ===
CREATE INDEX idx_status_lower ON events(lower(status));
ANALYZE events;
EXPLAIN (ANALYZE) SELECT count(*) FROM events WHERE lower(status) = 'paid';

\echo === размеры всех индексов ===
SELECT indexrelname, pg_size_pretty(pg_relation_size(indexrelid)) AS size
FROM pg_stat_user_indexes WHERE relname='events' ORDER BY pg_relation_size(indexrelid) DESC;
