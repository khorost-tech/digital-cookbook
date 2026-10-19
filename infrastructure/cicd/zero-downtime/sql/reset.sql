-- Возврат таблицы orders к исходной («старой») схеме. Демо меняют схему
-- (RENAME, ADD/DROP COLUMN, индексы), поэтому каждое начинается с этого reset —
-- иначе прогон зависел бы от того, чем закончился предыдущий.
DROP TABLE IF EXISTS orders CASCADE;

CREATE TABLE orders (
    id          bigserial PRIMARY KEY,
    customer    text        NOT NULL,
    amount_cents integer    NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO orders (customer, amount_cents)
SELECT 'customer-' || (g % 100), (g * 7) % 100000
FROM generate_series(1, 5000) AS g;
