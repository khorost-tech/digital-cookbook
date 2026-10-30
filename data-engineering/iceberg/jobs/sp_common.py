"""Общее для заданий Spark: сессия, таблица заказов из генератора, вывод итога."""

import json

from pyspark.sql import Row
from pyspark.sql.types import IntegerType, LongType, StringType, StructField, StructType, TimestampType

from generator import gen
from jobs.spark_session import session

SCHEMA = StructType([StructField("id", LongType()), StructField("customer_id", IntegerType()),
                     StructField("status", StringType()), StructField("amount", LongType()),
                     StructField("created_at", TimestampType())])


def orders_df(s, days=None):
    rows = [r for r in gen.orders() if days is None or r["created_at"].date().isoformat() in days]
    return s.createDataFrame([Row(**r) for r in rows], SCHEMA)


def fresh(s, name, props="", partition="days(created_at)"):
    s.sql("CREATE NAMESPACE IF NOT EXISTS lake.shop")
    s.sql("DROP TABLE IF EXISTS lake.shop.%s PURGE" % name)
    s.sql("CREATE TABLE lake.shop.%s (id bigint, customer_id int, status string, amount bigint, created_at timestamp) "
          "USING iceberg PARTITIONED BY (%s) %s" % (name, partition, props))


def one(s, q):
    return s.sql(q).collect()[0][0]


def emit(obj):
    print("RESULT " + json.dumps(obj, ensure_ascii=False, default=str), flush=True)
