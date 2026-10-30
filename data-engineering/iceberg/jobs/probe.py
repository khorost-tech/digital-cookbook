from jobs.spark_session import session
s = session("probe")
s.sql("CREATE TABLE IF NOT EXISTS lake.shop.probe (id bigint, v string) USING iceberg")
s.sql("INSERT INTO lake.shop.probe VALUES (1, 'a'), (2, 'b')")
print("rows", s.sql("SELECT count(*) FROM lake.shop.probe").collect()[0][0])
print("snapshots", s.sql("SELECT count(*) FROM lake.shop.probe.snapshots").collect()[0][0])
s.sql("DROP TABLE lake.shop.probe PURGE")
