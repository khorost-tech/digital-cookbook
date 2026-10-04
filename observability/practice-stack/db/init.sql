-- Схема подопытной системы. Намеренно простая: статьи серии про наблюдаемость,
-- а не про модель данных. Важно другое — запросы к этой схеме должны быть
-- достаточно разными, чтобы в трейсе были видны отдельные операции.

CREATE TABLE inventory (
    sku        TEXT PRIMARY KEY,
    name       TEXT        NOT NULL,
    quantity   INTEGER     NOT NULL CHECK (quantity >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE orders (
    order_id   TEXT PRIMARY KEY,
    sku        TEXT        NOT NULL REFERENCES inventory (sku),
    quantity   INTEGER     NOT NULL CHECK (quantity > 0),
    reserved   BOOLEAN     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX orders_sku_idx ON orders (sku);
CREATE INDEX orders_created_at_idx ON orders (created_at DESC);

-- Сид-данные детерминированы: нагрузочный сценарий обращается по этим SKU, и
-- ожидаемое число успешных заказов считается заранее.
INSERT INTO inventory (sku, name, quantity) VALUES
    ('SKU-0001', 'Гайка М8',            1000),
    ('SKU-0002', 'Болт М8x40',           800),
    ('SKU-0003', 'Шайба плоская М8',    5000),
    ('SKU-0004', 'Шпилька М8x100',       120),
    ('SKU-0005', 'Гайка самоконтрящаяся', 3),  -- заведомо мало: даёт reserved=false
    ('SKU-0006', 'Подшипник 6204',         0); -- нулевой остаток

-- Отдельный SKU для искусственных отказов бэкенда: по нему java-backend
-- намеренно отвечает ошибкой. Строка нужна, чтобы отказ был именно отказом
-- обработки, а не «товар не найден».
INSERT INTO inventory (sku, name, quantity) VALUES ('SKU-BOOM', 'Ошибка обработки', 1);
