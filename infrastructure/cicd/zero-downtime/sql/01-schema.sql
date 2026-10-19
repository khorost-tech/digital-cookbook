-- Исходная схема раздела zero-downtime. Демо переименовывают колонку
-- amount_cents -> total_cents: наивно (роняет старый код под нагрузкой) и через
-- expand-contract (без ошибок). Стартовое состояние — «старая» схема.
CREATE TABLE orders (
    id          bigserial PRIMARY KEY,
    customer    text        NOT NULL,
    amount_cents integer    NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Сид: 5000 строк, чтобы backfill в expand-contract шёл заметными батчами, а
-- CREATE INDEX на непустой таблице реально держал лок ощутимое время.
INSERT INTO orders (customer, amount_cents)
SELECT 'customer-' || (g % 100), (g * 7) % 100000
FROM generate_series(1, 5000) AS g;
