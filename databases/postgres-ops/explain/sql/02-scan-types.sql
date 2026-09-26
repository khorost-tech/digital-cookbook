-- Типы сканирования в плане. Селективность предиката управляет выбором узла.
-- Цель — увидеть в живую 4 разных узла и научиться их различать в выводе.

\echo === Seq Scan: низкоселективный предикат (~40% строк 'paid') ===
-- Индекс не помог бы: дешевле прочитать таблицу подряд, чем прыгать по heap.
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM orders WHERE status = 'paid';

\echo
\echo === Index Scan: высокоселективный предикат (~40 заказов на клиента) ===
CREATE INDEX IF NOT EXISTS idx_orders_customer ON orders(customer_id);
ANALYZE orders;
-- Index Cond + переход в heap за строками. Мало строк — узел выгоден.
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders WHERE customer_id = 12345;

\echo
\echo === Bitmap Heap Scan + Bitmap Index Scan: средняя селективность ===
-- Диапазон клиентов: строк много и они разбросаны по heap. Планировщик строит
-- битовую карту страниц (Bitmap Index Scan) и читает heap по порядку (Bitmap Heap Scan).
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders WHERE customer_id BETWEEN 10000 AND 10500;

\echo
\echo === Index Only Scan: запрос покрыт индексом, heap не нужен ===
-- Выбираем только колонку из индекса → Heap Fetches близко к 0.
EXPLAIN (ANALYZE, BUFFERS)
SELECT customer_id FROM orders WHERE customer_id = 12345;
