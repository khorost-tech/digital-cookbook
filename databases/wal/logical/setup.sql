-- Таблица-жертва, publication и логический слот репликации (pgoutput)
DROP TABLE IF EXISTS orders;
CREATE TABLE orders (id bigserial PRIMARY KEY, amount numeric, status text);

DROP PUBLICATION IF EXISTS wal_pub;
CREATE PUBLICATION wal_pub FOR TABLE orders;

SELECT pg_drop_replication_slot('wal_slot') FROM pg_replication_slots WHERE slot_name='wal_slot';
SELECT slot_name FROM pg_create_logical_replication_slot('wal_slot', 'pgoutput');
