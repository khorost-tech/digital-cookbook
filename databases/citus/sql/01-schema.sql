-- Модель: интернет-магазин. Тенант — customer_id, он же ключ шардирования.
--
-- ВАЖНО про выбор модели: ключ шардирования выбран так, чтобы типичные
-- запросы попадали в один шард (заказы одного покупателя лежат рядом с самим
-- покупателем). Обоснование выбора ключа в статье НЕ разбирается — это тема
-- соседней статьи про шардирование в MongoDB; здесь ключ дан как условие
-- задачи.

CREATE TABLE customers (
    customer_id BIGINT NOT NULL,
    name        TEXT   NOT NULL,
    city        TEXT   NOT NULL,
    PRIMARY KEY (customer_id)
);

CREATE TABLE orders (
    customer_id BIGINT      NOT NULL,   -- ключ шардирования, тот же что у customers
    order_id    BIGINT      NOT NULL,
    total       NUMERIC(12,2) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (customer_id, order_id)
);

-- Таблица, распределённая по ДРУГОМУ ключу — специально, чтобы join с orders
-- не мог быть локальным и требовал переброски данных между узлами.
--
-- ЖИВАЯ НАХОДКА (Task 1, проверено на Citus 14.1): PRIMARY KEY (shipment_id)
-- из исходного плана не проходит create_distributed_table('shipments',
-- 'order_id') — Citus требует, чтобы ключ шардирования входил в любой
-- UNIQUE/PRIMARY KEY/EXCLUDE. Составной ключ ниже — фактическое решение.
CREATE TABLE shipments (
    shipment_id BIGINT NOT NULL,
    order_id    BIGINT NOT NULL,
    carrier     TEXT   NOT NULL,
    PRIMARY KEY (shipment_id, order_id)
);

-- Маленький справочник: одинаков для всех и меняется редко.
CREATE TABLE carriers (
    carrier TEXT PRIMARY KEY,
    country TEXT NOT NULL
);
