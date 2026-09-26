-- Declarative partitioning: режем большую таблицу на управляемые куски.
-- Главный выигрыш в плане — partition pruning: планировщик отбрасывает партиции,
-- которые не могут содержать искомое, и не сканирует их вовсе.

-- ─────────────────────────── RANGE (по времени) ───────────────────────────
\echo === RANGE-партиционирование measurements по месяцам ===
DROP TABLE IF EXISTS measurements CASCADE;
CREATE TABLE measurements (
    id        bigserial,
    device_id int         NOT NULL,
    ts        timestamptz NOT NULL,
    value     float       NOT NULL
) PARTITION BY RANGE (ts);

-- 6 месячных партиций (2026-01 .. 2026-06).
CREATE TABLE measurements_2026_01 PARTITION OF measurements FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
CREATE TABLE measurements_2026_02 PARTITION OF measurements FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
CREATE TABLE measurements_2026_03 PARTITION OF measurements FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE measurements_2026_04 PARTITION OF measurements FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE measurements_2026_05 PARTITION OF measurements FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE measurements_2026_06 PARTITION OF measurements FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

SELECT setseed(0.77);
INSERT INTO measurements (device_id, ts, value)
SELECT (random()*1000)::int,
       '2026-01-01'::timestamptz + (random()*180) * interval '1 day',
       random()*100
FROM generate_series(1, 600000);
ANALYZE measurements;

\echo --- pruning: запрос за один месяц сканирует ТОЛЬКО одну партицию ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM measurements
WHERE ts >= '2026-03-01' AND ts < '2026-04-01';

\echo --- без условия на ключ партиционирования — Append по ВСЕМ партициям ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM measurements WHERE value > 99.5;

\echo === обслуживание: detach старой партиции — мгновенное «удаление» месяца ===
-- DETACH не удаляет данные (можно заархивировать), но убирает их из таблицы без
-- построчного DELETE и без bloat. Это ключевое эксплуатационное преимущество.
ALTER TABLE measurements DETACH PARTITION measurements_2026_01;
\echo --- теперь партиций 5; запрос за январь возвращает 0 без ошибки ---
SELECT count(*) AS jan_rows FROM measurements WHERE ts >= '2026-01-01' AND ts < '2026-02-01';
DROP TABLE measurements_2026_01;

\echo === attach новой партиции — добавляем июль ===
CREATE TABLE measurements_2026_07 (LIKE measurements INCLUDING DEFAULTS);
ALTER TABLE measurements ATTACH PARTITION measurements_2026_07 FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
SELECT count(*) AS parts FROM pg_inherits WHERE inhparent = 'measurements'::regclass;

-- ─────────────────────────── LIST (по региону) ────────────────────────────
\echo === LIST-партиционирование sales по региону ===
DROP TABLE IF EXISTS sales CASCADE;
CREATE TABLE sales (id bigserial, region text, amount numeric) PARTITION BY LIST (region);
CREATE TABLE sales_eu   PARTITION OF sales FOR VALUES IN ('EU');
CREATE TABLE sales_us   PARTITION OF sales FOR VALUES IN ('US');
CREATE TABLE sales_rest PARTITION OF sales DEFAULT;   -- всё остальное
SELECT setseed(0.78);
INSERT INTO sales (region, amount)
SELECT (ARRAY['EU','US','ASIA'])[1+(random()*2)::int], round((random()*1000)::numeric,2)
FROM generate_series(1, 100000);
ANALYZE sales;
\echo --- pruning по региону: сканируется только партиция EU ---
EXPLAIN (ANALYZE, BUFFERS) SELECT count(*) FROM sales WHERE region = 'EU';

-- ─────────────────────────── HASH (равномерно) ────────────────────────────
\echo === HASH-партиционирование accounts (равномерное распределение) ===
DROP TABLE IF EXISTS accounts CASCADE;
CREATE TABLE accounts (id bigint, balance numeric) PARTITION BY HASH (id);
CREATE TABLE accounts_p0 PARTITION OF accounts FOR VALUES WITH (MODULUS 4, REMAINDER 0);
CREATE TABLE accounts_p1 PARTITION OF accounts FOR VALUES WITH (MODULUS 4, REMAINDER 1);
CREATE TABLE accounts_p2 PARTITION OF accounts FOR VALUES WITH (MODULUS 4, REMAINDER 2);
CREATE TABLE accounts_p3 PARTITION OF accounts FOR VALUES WITH (MODULUS 4, REMAINDER 3);
INSERT INTO accounts SELECT g, 0 FROM generate_series(1, 100000) g;
ANALYZE accounts;
\echo --- равномерность: строк примерно поровну по 4 партициям ---
SELECT tableoid::regclass AS partition, count(*) FROM accounts GROUP BY 1 ORDER BY 1;
