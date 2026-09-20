-- Миграции плагина исполняются кластером при `ALTER PLUGIN ... MIGRATE TO`.
--
-- Таблица создаётся именно миграцией плагина, а не отдельным DDL: схема, от
-- которой зависит сервис, едет вместе с самим сервисом и откатывается вместе
-- с ним. Это и есть причина, по которой у плагина есть собственные миграции.
--
-- ВНИМАНИЕ: имена аннотаций, размечающих секции, ищутся по всему файлу. Если
-- упомянуть такое имя внутри обычного комментария, установка плагина падает с
-- сообщением "unexpected annotation, it must be at the start of the line" —
-- ровно это и случилось на стенде. Поэтому ниже они встречаются по одному
-- разу, только там, где действительно размечают секции.

-- pico.UP
CREATE TABLE products (
    id           UNSIGNED NOT NULL,
    sku          TEXT NOT NULL,
    title        TEXT NOT NULL,
    price_cents  UNSIGNED NOT NULL,
    category     TEXT NOT NULL,
    views        UNSIGNED NOT NULL,
    PRIMARY KEY (id)
)
USING memtx
DISTRIBUTED BY (id);

-- pico.DOWN
DROP TABLE products;
