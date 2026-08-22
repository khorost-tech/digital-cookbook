-- Наполняем два Kafka-топика через datagen (по одному INSERT-джобу на топик).
-- Выполнить в SQL client ДО join.sql.

-- Топик заказов.
CREATE TABLE orders_src (
    order_id  INT,
    user_id   INT,
    amount    INT
) WITH (
    'connector' = 'datagen',
    'rows-per-second' = '5',
    'fields.order_id.kind' = 'sequence', 'fields.order_id.start' = '1', 'fields.order_id.end' = '1000000',
    'fields.user_id.min' = '1', 'fields.user_id.max' = '100',
    'fields.amount.min' = '1', 'fields.amount.max' = '500'
);

CREATE TABLE orders (
    order_id  INT,
    user_id   INT,
    amount    INT,
    ts        TIMESTAMP(3),
    WATERMARK FOR ts AS ts - INTERVAL '5' SECOND
) WITH (
    'connector' = 'kafka', 'topic' = 'orders',
    'properties.bootstrap.servers' = 'kafka:9092',
    'properties.group.id' = 'flink-orders',
    'scan.startup.mode' = 'earliest-offset',
    'format' = 'json'
);

-- Топик платежей (та же схема order_id для джойна).
-- `method` — зарезервированное слово парсера Flink SQL, поэтому колонка везде в обратных кавычках.
CREATE TABLE payments_src (
    order_id  INT,
    `method`  STRING
) WITH (
    'connector' = 'datagen',
    'rows-per-second' = '5',
    'fields.order_id.kind' = 'sequence', 'fields.order_id.start' = '1', 'fields.order_id.end' = '1000000',
    'fields.method.length' = '4'
);

CREATE TABLE payments (
    order_id  INT,
    `method`  STRING,
    ts        TIMESTAMP(3),
    WATERMARK FOR ts AS ts - INTERVAL '5' SECOND
) WITH (
    'connector' = 'kafka', 'topic' = 'payments',
    'properties.bootstrap.servers' = 'kafka:9092',
    'properties.group.id' = 'flink-payments',
    'scan.startup.mode' = 'earliest-offset',
    'format' = 'json'
);

-- Два фидер-джоба (можно запустить пачкой через STATEMENT SET).
EXECUTE STATEMENT SET
BEGIN
  INSERT INTO orders   SELECT order_id, user_id, amount, CURRENT_TIMESTAMP FROM orders_src;
  INSERT INTO payments SELECT order_id, `method`, CURRENT_TIMESTAMP FROM payments_src;
END;
