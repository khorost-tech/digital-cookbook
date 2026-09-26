-- Антипаттерны: индекс есть, но запрос написан так, что им нельзя воспользоваться.
-- Диагноз ставится по плану: ищем Seq Scan + Filter там, где ждали Index Cond.
-- Каждый случай — пара «сломано → как надо».

CREATE INDEX IF NOT EXISTS idx_customers_name ON customers(name);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
ANALYZE customers, orders;

\echo ================= 1. функция по колонке ломает индекс =================
\echo --- СЛОМАНО: lower(name) — индекс по name не подходит, Seq Scan + Filter ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE lower(name) = 'ivan';
\echo --- КАК НАДО: прямое сравнение колонки — индекс задействован ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE name = 'ivan';

\echo
\echo ================= 2. вычисление/каст по колонке =================
\echo --- СЛОМАНО: created_at::date — приведение по колонке убивает btree ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders WHERE created_at::date = current_date;
\echo --- КАК НАДО: полуинтервал по самой колонке — Index Cond ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders
WHERE created_at >= current_date AND created_at < current_date + 1;

\echo
\echo ================= 3. OR: миф и реальность =================
\echo --- НЕ антипаттерн: оба операнда селективны и под индексами → BitmapOr ---
-- customer_id и created_at оба btree, оба выбирают мало строк.
-- Планировщик объединяет два Bitmap Index Scan узлом BitmapOr — индексы работают.
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders
WHERE customer_id = 12345
   OR created_at >= now() - interval '30 minutes';
\echo --- АНТИПАТТЕРН: один операнд без индекса (status) → Seq Scan на весь OR ---
-- Достаточно одной неиндексируемой ветви, чтобы весь OR ушёл в Seq Scan.
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders WHERE customer_id = 12345 OR status = 'cancelled';

\echo
\echo ================= 4. LIKE с ведущим % =================
\echo --- СЛОМАНО: ведущий wildcard — btree бесполезен, Seq Scan ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE name LIKE '%van%';
\echo --- КАК НАДО: якорь слева (для text_pattern_ops-индекса) ---
CREATE INDEX IF NOT EXISTS idx_customers_name_pat ON customers(name text_pattern_ops);
ANALYZE customers;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE name LIKE 'Iva%';
