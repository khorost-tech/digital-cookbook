-- Антипаттерны: индекс есть, но запрос написан так, что им нельзя воспользоваться.
-- Диагноз ставится по плану: ищем Seq Scan + Filter там, где ждали Index Cond.
-- Каждый случай — пара «сломано → как надо», где «как надо» обязано возвращать
-- ТЕ ЖЕ строки. Это проверяется явно: обе разности EXCEPT ALL должны быть пустыми
-- (count(*) не годится — одинаковое число строк не значит одинаковые строки).
-- Пары, которые ускоряют запрос ценой другого результата, помечены «НЕ ЗАМЕНА».

CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- Индексы-«лекарства» снимаются в начале, чтобы повторный прогон показывал
-- исходный план «сломано», а не уже вылеченный.
DROP INDEX IF EXISTS idx_customers_lower_name;
DROP INDEX IF EXISTS idx_customers_name_trgm;
CREATE INDEX IF NOT EXISTS idx_customers_name ON customers(name);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
ANALYZE customers, orders;

\echo ================= 1. функция по колонке ломает индекс =================
\echo --- СЛОМАНО: lower(name) — индекс по name не подходит, Seq Scan + Filter ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE lower(name) = 'ivan';
\echo --- НЕ ЗАМЕНА: name = «ivan» — индекс работает, но это другой запрос ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE name = 'ivan';
\echo --- проверка: name = «ivan» теряет строки «Ivan» ---
SELECT
  (SELECT count(*) FROM (SELECT * FROM customers WHERE lower(name) = 'ivan'
                         EXCEPT ALL
                         SELECT * FROM customers WHERE name = 'ivan') d) AS lost_rows,
  (SELECT count(*) FROM (SELECT * FROM customers WHERE name = 'ivan'
                         EXCEPT ALL
                         SELECT * FROM customers WHERE lower(name) = 'ivan') d) AS extra_rows;
\echo --- КАК НАДО: тот же запрос + expression-индекс на lower(name) ---
CREATE INDEX idx_customers_lower_name ON customers (lower(name));
ANALYZE customers;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE lower(name) = 'ivan';

\echo
\echo ================= 2. вычисление/каст по колонке =================
\echo --- СЛОМАНО: created_at::date — приведение по колонке убивает btree ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders WHERE created_at::date = current_date;
\echo --- КАК НАДО: полуинтервал по самой колонке — Index Cond ---
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders
WHERE created_at >= current_date AND created_at < current_date + 1;
\echo --- проверка эквивалентности: обе разности пусты ---
SELECT
  (SELECT count(*) FROM (SELECT id FROM orders WHERE created_at::date = current_date
                         EXCEPT ALL
                         SELECT id FROM orders
                         WHERE created_at >= current_date AND created_at < current_date + 1) d) AS lost_rows,
  (SELECT count(*) FROM (SELECT id FROM orders
                         WHERE created_at >= current_date AND created_at < current_date + 1
                         EXCEPT ALL
                         SELECT id FROM orders WHERE created_at::date = current_date) d) AS extra_rows;

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
\echo --- НЕ ЗАМЕНА: якорь слева (text_pattern_ops) — индекс работает, но это префиксный поиск ---
CREATE INDEX IF NOT EXISTS idx_customers_name_pat ON customers(name text_pattern_ops);
ANALYZE customers;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE name LIKE 'Iva%';
\echo --- проверка: префикс «Iva» теряет строки «ivan» ---
SELECT
  (SELECT count(*) FROM (SELECT * FROM customers WHERE name LIKE '%van%'
                         EXCEPT ALL
                         SELECT * FROM customers WHERE name LIKE 'Iva%') d) AS lost_rows,
  (SELECT count(*) FROM (SELECT * FROM customers WHERE name LIKE 'Iva%'
                         EXCEPT ALL
                         SELECT * FROM customers WHERE name LIKE '%van%') d) AS extra_rows;
\echo --- КАК НАДО: тот же запрос + триграммный GIN-индекс ---
CREATE INDEX idx_customers_name_trgm ON customers USING gin (name gin_trgm_ops);
ANALYZE customers;
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM customers WHERE name LIKE '%van%';
