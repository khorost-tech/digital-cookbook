DROP TABLE IF EXISTS events;
CREATE TABLE events (
    id          bigserial PRIMARY KEY,
    user_id     bigint      NOT NULL,
    status      text        NOT NULL,
    amount      numeric(12,2) NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL
);
-- 2 млн строк; seed через setseed для воспроизводимости
SELECT setseed(0.42);
INSERT INTO events (user_id, status, amount, payload, created_at)
SELECT
    (random()*100000)::bigint,
    (ARRAY['new','paid','shipped','cancelled','refunded'])[1 + (random()*4)::int],
    round((random()*1000)::numeric, 2),
    jsonb_build_object('tag',(ARRAY['a','b','c','d'])[1+(random()*3)::int],'n',(random()*100)::int),
    now() - (random()*365) * interval '1 day'
FROM generate_series(1, 2000000);
ANALYZE events;
SELECT count(*) AS rows, pg_size_pretty(pg_relation_size('events')) AS heap FROM events;
