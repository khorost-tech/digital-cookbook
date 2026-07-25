CREATE TABLE orders (
    id            BIGSERIAL PRIMARY KEY,
    customer_name TEXT   NOT NULL,
    amount_cents  BIGINT NOT NULL,
    status        TEXT   NOT NULL
);
