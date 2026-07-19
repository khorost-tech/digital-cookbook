CREATE INDEX idx_user_status ON events(user_id, status);

-- Изолируем составной индекс idx_user_status. К этому моменту на events накопились
-- индексы из 01/03, которые затенили бы idx_user_status в позитивных запросах и увели
-- бы негативный запрос от честного Seq Scan:
--   idx_events_user, idx_events_user_cov — по user_id: планировщик предпочёл бы их
--       для «WHERE user_id=...», и демонстрировался бы не составной индекс;
--   idx_paid_partial (partial WHERE status='paid') — покрыл бы «... AND status='paid'»
--       и «status='paid'» вместо составного;
--   idx_status_hash (hash по status) — обслужил бы «status='paid'» и спрятал бы Seq Scan.
-- После дропа единственный кандидат по (user_id, status) — idx_user_status, и все пять
-- запросов показывают ровно его поведение (left-prefix работает / status в одиночку — нет).
DROP INDEX IF EXISTS idx_events_user;
DROP INDEX IF EXISTS idx_events_user_cov;
DROP INDEX IF EXISTS idx_paid_partial;
DROP INDEX IF EXISTS idx_status_hash;
ANALYZE events;

\echo === left-prefix: WHERE user_id=... (использует индекс) ===
EXPLAIN (ANALYZE) SELECT * FROM events WHERE user_id = 555;

\echo === left-prefix: WHERE user_id=... AND status=... (полностью) ===
EXPLAIN (ANALYZE) SELECT * FROM events WHERE user_id = 555 AND status = 'paid';

\echo === НЕ prefix: только WHERE status=... (composite не подходит → Seq Scan) ===
EXPLAIN (ANALYZE) SELECT count(*) FROM events WHERE status = 'paid';

\echo === игнор: функция по колонке без expression-индекса ===
EXPLAIN (ANALYZE) SELECT count(*) FROM events WHERE upper(status) = 'PAID';

\echo === игнор: приведение типа (user_id::text) ===
EXPLAIN (ANALYZE) SELECT * FROM events WHERE user_id::text = '555';
