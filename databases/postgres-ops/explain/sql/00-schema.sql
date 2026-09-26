-- Стенд explain: датасет интернет-магазина. Отдельный от db-indexes (там таблица events).
-- Здесь фокус на ЧТЕНИИ плана EXPLAIN(ANALYZE,BUFFERS), а не на выборе индекса.
-- Воспроизводимость: setseed перед каждым массивом random().

\set ON_ERROR_STOP on
\timing on

DROP TABLE IF EXISTS order_items, orders, promotions, products, customers CASCADE;

-- ── customers ──────────────────────────────────────────────────────────────
-- name смешанного регистра: часть 'ivan' (lower), часть 'Ivan' — для антипаттернов (сц.08).
CREATE TABLE customers (
    id         bigserial PRIMARY KEY,
    name       text        NOT NULL,
    city       text        NOT NULL,
    created_at timestamptz NOT NULL
);
SELECT setseed(0.11);
INSERT INTO customers (name, city, created_at)
SELECT
    (ARRAY['ivan','Ivan','petr','Petr','maria','Maria','olga','Olga','sergey','Sergey'])[1 + (random()*9)::int],
    (ARRAY['Москва','Санкт-Петербург','Казань','Новосибирск','Екатеринбург'])[1 + (random()*4)::int],
    now() - (random()*1000) * interval '1 day'
FROM generate_series(1, 50000);

-- ── products ───────────────────────────────────────────────────────────────
-- title — русские товарные слова (для GiN полнотекста, сц.04);
-- attrs jsonb — brand/color/size (для GiN jsonb, сц.04);
-- search — генерируемая tsvector-колонка (russian);
-- price — для kNN GiST (сц.05).
CREATE TABLE products (
    id     bigserial PRIMARY KEY,
    title  text          NOT NULL,
    attrs  jsonb         NOT NULL,
    price  numeric(10,2) NOT NULL,
    search tsvector GENERATED ALWAYS AS (to_tsvector('russian', title)) STORED
);
SELECT setseed(0.22);
INSERT INTO products (title, attrs, price)
SELECT
    (ARRAY['ноутбук','смартфон','мышь','клавиатура','монитор'])[1 + (random()*4)::int]
      || ' ' ||
    (ARRAY['игровой','офисный','беспроводной','компактный','премиум'])[1 + (random()*4)::int],
    jsonb_build_object(
        'brand', (ARRAY['acme','globex','initech','umbrella','stark'])[1 + (random()*4)::int],
        'color', (ARRAY['black','white','silver','blue'])[1 + (random()*3)::int],
        'size',  (random()*40)::int
    ),
    round((random()*990 + 10)::numeric, 2)
FROM generate_series(1, 100000);

-- ── orders ─────────────────────────────────────────────────────────────────
-- created_at МОНОТОННО возрастает с id → физическая корреляция ~1 (для BRIN, сц.06).
-- status — низкоселективный 'paid' (~40%), для scan-типов (сц.02).
CREATE TABLE orders (
    id          bigserial PRIMARY KEY,
    customer_id bigint      NOT NULL,
    status      text        NOT NULL,
    total       numeric(10,2) NOT NULL,
    created_at  timestamptz NOT NULL
);
SELECT setseed(0.33);
INSERT INTO orders (customer_id, status, total, created_at)
SELECT
    1 + (random()*49999)::int,
    (ARRAY['paid','paid','new','shipped','cancelled'])[1 + (random()*4)::int],
    round((random()*500 + 5)::numeric, 2),
    -- база 2 года назад + i * шаг: строго возрастает → correlation(created_at) ≈ 1
    (now() - interval '730 days') + (i * (interval '730 days' / 2000000))
FROM generate_series(1, 2000000) AS g(i);

-- ── order_items ────────────────────────────────────────────────────────────
-- ~3 позиции на заказ → ~6M строк. Для join/N+1 в pg_stat_statements (сц.09).
CREATE TABLE order_items (
    id         bigserial PRIMARY KEY,
    order_id   bigint NOT NULL,
    product_id bigint NOT NULL,
    qty        int    NOT NULL,
    price      numeric(10,2) NOT NULL
);
SELECT setseed(0.44);
INSERT INTO order_items (order_id, product_id, qty, price)
SELECT
    1 + (random()*1999999)::int,
    1 + (random()*99999)::int,
    1 + (random()*4)::int,
    round((random()*300 + 1)::numeric, 2)
FROM generate_series(1, 6000000);

-- ── promotions ─────────────────────────────────────────────────────────────
-- period tstzrange — для GiST range (сц.05). Короткие (7..30 дней) окна, РАЗБРОСАННЫЕ
-- по двум годам, чтобы пересечение с конкретной датой было селективным (иначе GiST
-- нечего показывать: если каждый период накрывает now(), && совпадёт со всеми).
CREATE TABLE promotions (
    id         bigserial PRIMARY KEY,
    product_id bigint NOT NULL,
    period     tstzrange NOT NULL
);
SELECT setseed(0.55);
INSERT INTO promotions (product_id, period)
SELECT
    1 + (random()*99999)::int,
    tstzrange(s, s + ((7 + random()*23) || ' days')::interval)
FROM (
    SELECT (now() - interval '730 days') + (random()*730) * interval '1 day' AS s
    FROM generate_series(1, 100000)
) g;

ANALYZE customers, products, orders, order_items, promotions;

\echo === размеры таблиц ===
SELECT relname AS tbl,
       to_char((SELECT reltuples::bigint FROM pg_class WHERE oid = c.oid), 'FM999,999,999') AS approx_rows,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size
FROM pg_class c
WHERE relname IN ('customers','products','orders','order_items','promotions')
  AND relkind = 'r'
ORDER BY pg_total_relation_size(c.oid) DESC;
