-- Схема для интеграционного и failures-тестов. Один набор миграций на оба
-- сценария: каждый тест поднимает свой чистый Postgres-контейнер и мигрирует
-- его с нуля, поэтому конфликтов между таблицами нет.

-- integration/: заказы. external_id уникален на уровне БД — именно этот
-- constraint мок в юнит-тесте не воспроизвёл бы.
CREATE TABLE orders (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    external_id TEXT           NOT NULL UNIQUE,
    amount      NUMERIC(12, 2) NOT NULL
);

-- failures/: счёт (эффект обработки) и журнал обработанных событий
-- (ключ идемпотентности). Повторная доставка одного event_id не должна
-- применить эффект второй раз.
CREATE TABLE accounts (
    id      TEXT   PRIMARY KEY,
    balance BIGINT NOT NULL
);

CREATE TABLE processed_events (
    event_id     TEXT        PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
