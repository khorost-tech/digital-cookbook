-- Схема к серии статей "ScyllaDB: глубокое погружение".
-- keyspace telemetry — общий датасет-телеметрия (устройства, сутки, метрики)
-- для всех стендов серии. RF=3 на единственный ДЦ (datacenter1), NTS — так
-- принято для ScyllaDB/Cassandra даже на одном ДЦ (миграция на мульти-DC без
-- ALTER KEYSPACE на смену стратегии).

CREATE KEYSPACE IF NOT EXISTS telemetry
  WITH replication = {'class':'NetworkTopologyStrategy','replication_factor':3};

-- Хорошая модель: партиция ограничена сутками устройства, кластеризация по времени.
CREATE TABLE IF NOT EXISTS telemetry.readings (
  device_id text, day date, event_time timestamp,
  metric text, value double, region text, status text,
  PRIMARY KEY ((device_id, day), event_time)
) WITH CLUSTERING ORDER BY (event_time DESC);

-- Плохая модель (для #1): партиция только по device_id -> неограниченный рост.
CREATE TABLE IF NOT EXISTS telemetry.readings_bad (
  device_id text, event_time timestamp,
  metric text, value double, region text, status text,
  PRIMARY KEY (device_id, event_time)
) WITH CLUSTERING ORDER BY (event_time DESC);

-- Ещё хуже (hot partition, для #1): партиция по region -> 4 гигантские партиции.
CREATE TABLE IF NOT EXISTS telemetry.readings_hot (
  region text, event_time timestamp, device_id text,
  metric text, value double, status text,
  PRIMARY KEY (region, event_time, device_id)
) WITH CLUSTERING ORDER BY (event_time DESC);
