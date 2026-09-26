-- Анатомия вывода EXPLAIN. Один запрос, два режима: оценка → факт.
-- Что читать: cost=СТАРТ..ИТОГ, rows (оценка), width; actual time, actual rows, loops;
-- Buffers: shared hit (из кеша) / read (с диска); Planning/Execution Time.

\echo === 1. EXPLAIN без выполнения: только оценка планировщика ===
-- rows/cost — предсказание. Ничего не выполняется, факта нет.
EXPLAIN
SELECT * FROM orders
WHERE status = 'paid' AND created_at > now() - interval '30 days';

\echo
\echo === 2. EXPLAIN (ANALYZE, BUFFERS): реальное выполнение ===
-- Теперь виден actual rows против rows (оценка) и обращения к буферам.
-- Расхождение estimated/actual — первый сигнал проблем со статистикой (см. сц.07).
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM orders
WHERE status = 'paid' AND created_at > now() - interval '30 days';

\echo
\echo === 3. Тот же план в JSON — как это читают инструменты ===
EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
SELECT * FROM orders
WHERE status = 'paid' AND created_at > now() - interval '30 days';
