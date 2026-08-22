-- Стриминговый interval join двух Kafka-топиков: заказ + платёж по order_id
-- в окне ±1 минута по event-time. Это и есть джоба, на которой делаем savepoint/rescale.
-- Требует таблицы orders/payments из feeders.sql (в той же сессии SQL client).

CREATE TABLE enriched (
    order_id INT,
    user_id  INT,
    amount   INT,
    `method` STRING
) WITH (
    'connector' = 'kafka', 'topic' = 'orders-enriched',
    'properties.bootstrap.servers' = 'kafka:9092',
    'format' = 'json',
    'sink.delivery-guarantee' = 'exactly-once',
    'sink.transactional-id-prefix' = 'enriched-tx'
);

INSERT INTO enriched
SELECT o.order_id, o.user_id, o.amount, p.`method`
FROM orders o
JOIN payments p
  ON o.order_id = p.order_id
 AND p.ts BETWEEN o.ts - INTERVAL '1' MINUTE AND o.ts + INTERVAL '1' MINUTE;
