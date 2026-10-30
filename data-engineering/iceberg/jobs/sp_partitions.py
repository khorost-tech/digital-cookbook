"""Ось 6 (Iceberg): эволюция партиций и скрытое партиционирование.

Таблица по дню (days(created_at)), 01–07.09; смена на месяц (months(created_at)),
08–14.09. Счёт по метаданной таблице files: файлов по спецификации до и после,
переписаны ли старые. Затем — сколько файлов планирует запрос с фильтром по
времени заказа за 03.09 (Spark: число файлов в скане из метрик плана).

    spark-submit jobs/sp_partitions.py
"""

from jobs.sp_common import emit, fresh, one, orders_df
from jobs.spark_session import session

s = session("partitions")
t = "lake.shop.part_t"
fresh(s, "part_t")
first = ["2026-09-%02d" % d for d in range(1, 8)]
second = ["2026-09-%02d" % d for d in range(8, 15)]
orders_df(s, first).writeTo(t).append()
files_before = [r[0] for r in s.sql("SELECT file_path FROM %s.files" % t).collect()]
s.sql("ALTER TABLE %s REPLACE PARTITION FIELD days(created_at) WITH months(created_at)" % t)
orders_df(s, second).writeTo(t).append()
by_spec = {str(r[0]): r[1] for r in s.sql("SELECT spec_id, count(*) FROM %s.files GROUP BY 1" % t).collect()}
files_after = [r[0] for r in s.sql("SELECT file_path FROM %s.files" % t).collect()]
rows = one(s, "SELECT count(*) FROM %s" % t)
# сколько файлов попадает в скан запроса с фильтром по времени заказа
q = s.sql("SELECT count(*) FROM %s WHERE created_at >= '2026-09-03' AND created_at < '2026-09-04'" % t)
filtered_rows = q.collect()[0][0]
scan = [p for p in q._jdf.queryExecution().executedPlan().toString().split("\n") if "BatchScan" in p]
emit({"rows": rows, "files_total": len(files_after), "files_spec": by_spec,
      "old_files_kept": len(set(files_before) & set(files_after)), "old_files": len(files_before),
      "filtered_rows": filtered_rows, "scan": scan[0][:200] if scan else ""})
