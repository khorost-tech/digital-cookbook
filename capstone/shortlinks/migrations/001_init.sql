-- Ссылки, outbox событий создания, агрегаты кликов и учёт обработанных событий.
CREATE TABLE IF NOT EXISTS links (
    code       text PRIMARY KEY,
    url        text NOT NULL,
    created_at timestamptz NOT NULL
);

-- Событие создания ссылки пишется в той же транзакции, что и ссылка; ретранслятор
-- публикует его в брокер и отмечает published_at.
CREATE TABLE IF NOT EXISTS outbox (
    id           bigserial PRIMARY KEY,
    subject      text NOT NULL,
    payload      jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- Агрегат кликов по минутам — проекция, которую строит consumer.
CREATE TABLE IF NOT EXISTS clicks_by_minute (
    code   text NOT NULL,
    minute timestamptz NOT NULL,
    clicks bigint NOT NULL,
    PRIMARY KEY (code, minute)
);

-- Обработанные события: повторная доставка того же события не учитывается.
CREATE TABLE IF NOT EXISTS processed_events (
    event_id     text PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);

-- Какие события создания ссылок получила аналитика (сверка outbox против прямой публикации).
CREATE TABLE IF NOT EXISTS seen_links (
    code    text PRIMARY KEY,
    seen_at timestamptz NOT NULL DEFAULT now()
);
