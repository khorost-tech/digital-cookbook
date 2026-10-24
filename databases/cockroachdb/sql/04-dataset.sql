-- Датасет под сценарий планов запросов.
--
-- Две таблицы, соединяемые по ключу: 50 000 покупателей и 500 000 заказов.
-- Объём подобран так, чтобы запрос шёл сотни миллисекунд, а не микросекунды:
-- на микросекундах следующие замеры мерили бы шум, а не разницу планов.

USE bank;

CREATE TABLE IF NOT EXISTS customers (
    customer_id INT8 PRIMARY KEY,
    city        STRING NOT NULL,
    segment     STRING NOT NULL
);

CREATE TABLE IF NOT EXISTS orders (
    order_id    INT8 PRIMARY KEY,
    customer_id INT8 NOT NULL,
    total       DECIMAL(12, 2) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

INSERT INTO customers (customer_id, city, segment)
SELECT g,
       (ARRAY['Moscow', 'SPb', 'Kazan', 'Novosibirsk', 'Ekaterinburg'])[1 + (g % 5)],
       (ARRAY['retail', 'smb', 'enterprise'])[1 + (g % 3)]
FROM generate_series(1, 50000) AS g
ON CONFLICT (customer_id) DO NOTHING;
