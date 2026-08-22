-- Оконная агрегация (состояние в RocksDB) -> Kafka exactly-once.
-- Результат каждого окна append-only, поэтому обычный kafka-сток + 2PC даёт exactly-once.

CREATE TEMPORARY TABLE orders (
    user_id    INT,
    amount     INT,
    event_time AS CAST(CURRENT_TIMESTAMP AS TIMESTAMP(3)),
    WATERMARK FOR event_time AS event_time - INTERVAL '2' SECOND
) WITH (
    'connector' = 'datagen',
    'rows-per-second' = '10',
    'fields.user_id.min' = '1',
    'fields.user_id.max' = '3',
    'fields.amount.min' = '1',
    'fields.amount.max' = '100'
);

-- Сток в Kafka с гарантией exactly-once (транзакции + привязка к чекпоинту).
CREATE TEMPORARY TABLE user_totals (
    user_id      INT,
    window_end   TIMESTAMP(3),
    total_amount BIGINT
) WITH (
    'connector' = 'kafka',
    'topic' = 'user-totals',
    'properties.bootstrap.servers' = 'kafka:9092',
    'format' = 'json',
    'sink.delivery-guarantee' = 'exactly-once',
    'sink.transactional-id-prefix' = 'user-totals-tx'
);

-- Состояние окна живёт в RocksDB; при рестарте восстанавливается из чекпоинта.
INSERT INTO user_totals
SELECT user_id, window_end, SUM(amount) AS total_amount
FROM TABLE(
    TUMBLE(TABLE orders, DESCRIPTOR(event_time), INTERVAL '10' SECOND)
)
GROUP BY user_id, window_start, window_end;
