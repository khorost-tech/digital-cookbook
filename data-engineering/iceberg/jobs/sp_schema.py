"""Ось 5 (Iceberg): переименование колонки; удаление и повторное добавление
колонки с тем же именем. Старые строки — 01–07.09, новые после изменения — 08.09.

    spark-submit jobs/sp_schema.py
"""

from jobs.sp_common import emit, fresh, one, orders_df
from jobs.spark_session import session

s = session("schema")
t = "lake.shop.schema_t"
fresh(s, "schema_t")
old_days = ["2026-09-%02d" % d for d in range(1, 8)]
orders_df(s, old_days).writeTo(t).append()
old = one(s, "SELECT count(*) FROM %s" % t)
control = one(s, "SELECT count(status) FROM %s" % t)
s.sql("ALTER TABLE %s RENAME COLUMN status TO order_status" % t)
renamed_old_values = one(s, "SELECT count(order_status) FROM %s" % t)
s.sql("ALTER TABLE %s DROP COLUMN customer_id" % t)
s.sql("ALTER TABLE %s ADD COLUMN customer_id int" % t)
readded_old_values = one(s, "SELECT count(customer_id) FROM %s" % t)
emit({"old_rows": old, "control_values": control, "renamed_old_values": renamed_old_values,
      "readded_old_values": readded_old_values})
