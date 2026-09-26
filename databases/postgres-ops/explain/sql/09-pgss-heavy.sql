-- pg_stat_statements: поиск тяжёлых запросов НЕ по одному плану, а по агрегату.
-- EXPLAIN отвечает на «почему медленно ЭТОТ запрос»; pg_stat_statements — на
-- «какие запросы вообще съедают время в проде». Расширение нормализует запросы
-- (константы → $1) и копит по ним счётчики: calls, total/mean time, rows, буферы.

CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
SELECT pg_stat_statements_reset();

\echo === генерируем смешанную нагрузку (разнохарактерные запросы) ===
-- лёгкие точечные
SELECT * FROM orders WHERE customer_id = 111;
SELECT * FROM orders WHERE customer_id = 222;
SELECT * FROM orders WHERE customer_id = 333;
-- тяжёлый full-scan агрегат
SELECT status, count(*), avg(total) FROM orders GROUP BY status;
-- тяжёлый join без индекса на order_items.order_id
SELECT o.status, count(*)
FROM orders o JOIN order_items oi ON oi.order_id = o.id
WHERE o.created_at > now() - interval '90 days'
GROUP BY o.status;
-- средний по цене
SELECT count(*) FROM products WHERE price > 900;

\echo
\echo === топ запросов по суммарному времени (total_exec_time) ===
SELECT calls,
       round(total_exec_time::numeric, 1)  AS total_ms,
       round(mean_exec_time::numeric, 2)   AS mean_ms,
       rows,
       left(regexp_replace(query, '\s+', ' ', 'g'), 60) AS query
FROM pg_stat_statements
WHERE query NOT LIKE '%pg_stat_statements%'
ORDER BY total_exec_time DESC
LIMIT 10;
