\timing on
-- база: пустая таблица, вставка 500k строк без индексов
DROP TABLE IF EXISTS wa; CREATE TABLE wa (id bigserial, a bigint, b text, c numeric, d timestamptz, e jsonb);
SELECT setseed(0.42);
\echo === 0 индексов ===
INSERT INTO wa (a,b,c,d,e) SELECT (random()*1e6)::bigint,(random()*1e6)::text,round((random()*1000)::numeric,2),now()-(random()*365)*interval '1 day',jsonb_build_object('n',(random()*100)::int) FROM generate_series(1,500000);
SELECT pg_size_pretty(pg_total_relation_size('wa')) AS total_0idx;

DROP TABLE wa; CREATE TABLE wa (id bigserial, a bigint, b text, c numeric, d timestamptz, e jsonb);
CREATE INDEX wa1 ON wa(a);
SELECT setseed(0.42);
\echo === 1 индекс ===
INSERT INTO wa (a,b,c,d,e) SELECT (random()*1e6)::bigint,(random()*1e6)::text,round((random()*1000)::numeric,2),now()-(random()*365)*interval '1 day',jsonb_build_object('n',(random()*100)::int) FROM generate_series(1,500000);
SELECT pg_size_pretty(pg_total_relation_size('wa')) AS total_1idx;

DROP TABLE wa; CREATE TABLE wa (id bigserial, a bigint, b text, c numeric, d timestamptz, e jsonb);
CREATE INDEX wa1 ON wa(a); CREATE INDEX wa2 ON wa(b); CREATE INDEX wa3 ON wa(d);
SELECT setseed(0.42);
\echo === 3 индекса ===
INSERT INTO wa (a,b,c,d,e) SELECT (random()*1e6)::bigint,(random()*1e6)::text,round((random()*1000)::numeric,2),now()-(random()*365)*interval '1 day',jsonb_build_object('n',(random()*100)::int) FROM generate_series(1,500000);
SELECT pg_size_pretty(pg_total_relation_size('wa')) AS total_3idx;

DROP TABLE wa; CREATE TABLE wa (id bigserial, a bigint, b text, c numeric, d timestamptz, e jsonb);
CREATE INDEX wa1 ON wa(a); CREATE INDEX wa2 ON wa(b); CREATE INDEX wa3 ON wa(d); CREATE INDEX wa4 ON wa(c); CREATE INDEX wa5 ON wa((a%1000)); CREATE INDEX wa6 ON wa USING gin(e);
SELECT setseed(0.42);
\echo === 6 индексов ===
INSERT INTO wa (a,b,c,d,e) SELECT (random()*1e6)::bigint,(random()*1e6)::text,round((random()*1000)::numeric,2),now()-(random()*365)*interval '1 day',jsonb_build_object('n',(random()*100)::int) FROM generate_series(1,500000);
SELECT pg_size_pretty(pg_total_relation_size('wa')) AS total_6idx;
