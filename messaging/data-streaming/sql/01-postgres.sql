-- Схема демо-магазина: бизнес-таблица + transactional outbox.
-- Debezium читает ИМЕННО outbox (не orders) — событие пишется в одной транзакции с заказом.
CREATE TABLE orders (
    id          bigserial PRIMARY KEY,
    customer_id bigint      NOT NULL,
    amount      numeric(12,2) NOT NULL,
    status      text        NOT NULL DEFAULT 'new',
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Формат outbox под Debezium outbox event router (SMT):
--   aggregatetype → имя топика, aggregateid → ключ события, payload → тело.
CREATE TABLE outbox (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregatetype text  NOT NULL,
    aggregateid   text  NOT NULL,
    type          text  NOT NULL,
    payload       jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Логическая репликация: pgoutput — встроенный плагин (дефолт Debezium 2.x+),
-- wal2json не нужен. Публикация ограничена outbox: CDC ловит только события,
-- а не каждый UPDATE бизнес-таблицы.
CREATE PUBLICATION dbz_pub FOR TABLE outbox;

-- REPLICA IDENTITY FULL: чтобы в событии были все колонки (иначе только PK).
ALTER TABLE outbox REPLICA IDENTITY FULL;
