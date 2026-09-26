-- Таблица для демонстраций репликации и failover. Пишем в лидера (HAProxy :5000).
DROP TABLE IF EXISTS ha_demo;
CREATE TABLE ha_demo (id bigserial PRIMARY KEY, val text, ts timestamptz DEFAULT now());
INSERT INTO ha_demo (val) SELECT 'seed' || g FROM generate_series(1, 100) g;
SELECT count(*) AS seeded FROM ha_demo;
