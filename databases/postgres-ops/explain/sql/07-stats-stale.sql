-- Устаревшая статистика ломает оценку — и это видно в самом плане.
-- Планировщик масштабирует ОБЩЕЕ число строк по размеру файла, но РАСПРЕДЕЛЕНИЕ
-- (какая доля строк подходит под предикат) берёт из последнего ANALYZE. Если данные
-- перекосились, оценка доли врёт, пока не переанализируешь.
-- autovacuum_enabled=false — чтобы autovacuum не «починил» статистику за спиной.

DROP TABLE IF EXISTS fresh_orders;
CREATE TABLE fresh_orders (id bigserial PRIMARY KEY, status text NOT NULL)
    WITH (autovacuum_enabled = false);

\echo === фаза 1: active очень редок (~1%), собираем статистику ===
SELECT setseed(0.66);
INSERT INTO fresh_orders(status)
SELECT CASE WHEN random() < 0.01 THEN 'active' ELSE 'archived' END
FROM generate_series(1, 300000);
CREATE INDEX idx_fresh_status ON fresh_orders(status);
ANALYZE fresh_orders;

\echo --- оценка и факт совпадают: статистика свежая ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM fresh_orders WHERE status = 'active';

\echo
\echo === фаза 2: наводняем active (+300k), статистику НЕ обновляем ===
INSERT INTO fresh_orders(status)
SELECT 'active' FROM generate_series(1, 300000);

\echo --- planner всё ещё думает, что active ~1%: rows (оценка) << actual rows ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM fresh_orders WHERE status = 'active';

\echo
\echo === лечение: ANALYZE — и оценка сходится с фактом, план может смениться ===
ANALYZE fresh_orders;
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM fresh_orders WHERE status = 'active';
