-- Схема, написанная как для обычного PostgreSQL.
--
-- Она СПЕЦИАЛЬНО содержит конструкции, которые в CockroachDB ведут себя
-- иначе или не поддерживаются. Задача сценария — накатить её по одному
-- оператору и записать, что именно отказало, а не подобрать заранее
-- работающий вариант: смысл замера в списке отличий, а не в зелёном логе.
--
-- Перед каждым оператором стоит строка «-- EXPECT: pass|fail» — ожидаемый
-- исход. Скрипт сверяет фактический результат с ним поштучно: считать
-- общее число отказов недостаточно, оно совпадёт и при перепутанных
-- местами результатах.
--
-- Операторы разрезаются по строке-разделителю (см. awk в scripts/06-ops.sh).
-- Разделитель намеренно не упоминается здесь дословно: в первой редакции
-- он был написан внутри этого самого комментария, и awk разрезал файл
-- прямо по объяснению — база не создалась, а половина «отказов» оказалась
-- следствием ошибки разбора, а не несовместимости.

-- EXPECT: pass
CREATE DATABASE IF NOT EXISTS shop
-- ##SPLIT##
-- EXPECT: pass
CREATE EXTENSION IF NOT EXISTS pg_trgm
-- ##SPLIT##
-- EXPECT: pass
CREATE TABLE shop.public.customers (
    id       SERIAL PRIMARY KEY,
    email    TEXT NOT NULL UNIQUE,
    created  TIMESTAMPTZ NOT NULL DEFAULT now()
)
-- ##SPLIT##
-- EXPECT: fail
CREATE TABLE shop.public.prices (
    id     INT PRIMARY KEY,
    amount MONEY NOT NULL
)
-- ##SPLIT##
-- EXPECT: fail
CREATE TABLE shop.public.docs (
    id   INT PRIMARY KEY,
    body XML
)
-- ##SPLIT##
-- EXPECT: fail
CREATE TABLE shop.public.orders (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id INT NOT NULL REFERENCES shop.public.customers (id) DEFERRABLE INITIALLY DEFERRED,
    total       NUMERIC(12, 2) NOT NULL,
    created     TIMESTAMPTZ NOT NULL DEFAULT now()
)
-- ##SPLIT##
-- EXPECT: pass
CREATE TABLE shop.public.orders_ok (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    customer_id INT NOT NULL REFERENCES shop.public.customers (id),
    total       NUMERIC(12, 2) NOT NULL,
    created     TIMESTAMPTZ NOT NULL DEFAULT now()
)
-- ##SPLIT##
-- EXPECT: pass
CREATE INDEX orders_recent_idx ON shop.public.orders_ok (created) WHERE total > 1000
-- ##SPLIT##
-- EXPECT: pass
CREATE FUNCTION shop.public.touch_order() RETURNS TRIGGER AS $$
BEGIN
    NEW.created := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql
-- ##SPLIT##
-- EXPECT: pass
CREATE TRIGGER orders_touch BEFORE INSERT ON shop.public.orders_ok
    FOR EACH ROW EXECUTE FUNCTION shop.public.touch_order()
-- ##SPLIT##
-- EXPECT: pass
CREATE MATERIALIZED VIEW shop.public.customer_totals AS
    SELECT c.id, count(o.id) AS orders, coalesce(sum(o.total), 0) AS total
    FROM shop.public.customers c
    LEFT JOIN shop.public.orders_ok o ON o.customer_id = c.id
    GROUP BY c.id
-- ##SPLIT##
-- EXPECT: fail
CREATE RULE orders_no_delete AS ON DELETE TO shop.public.orders_ok DO INSTEAD NOTHING
-- ##SPLIT##
-- EXPECT: fail
SELECT pg_advisory_lock(42)
-- ##SPLIT##
-- EXPECT: fail
SELECT pg_size_pretty(pg_database_size('shop'))
