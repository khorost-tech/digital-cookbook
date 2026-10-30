"""Ось 7 (Iceberg): мелкие файлы и обслуживание, по фазам — между фазами раннер
считает объекты таблицы в хранилище.

    spark-submit jobs/sp_maint.py write | compact | expire | count
"""

import sys

from jobs.sp_common import emit, fresh, one, orders_df
from jobs.spark_session import session

phase = sys.argv[1]
s = session("maint-" + phase)
t = "lake.shop.maint_t"
if phase == "write":
    fresh(s, "maint_t")
    df = orders_df(s, ["2026-09-01"]).limit(50).collect()
    for r in df:  # 50 вставок по одной строке — 50 коммитов
        s.createDataFrame([r], orders_df(s, ["2026-09-01"]).schema).writeTo(t).append()
elif phase == "compact":
    s.sql("CALL lake.system.rewrite_data_files(table => 'shop.maint_t', options => map('min-input-files', '2'))")
elif phase == "expire":
    ts = one(s, "SELECT max(committed_at) FROM %s.snapshots" % t)
    s.sql("CALL lake.system.expire_snapshots(table => 'shop.maint_t', older_than => TIMESTAMP '%s', "
          "retain_last => 1)" % ts)
emit({"phase": phase, "rows": one(s, "SELECT count(*) FROM %s" % t),
      "data_files": one(s, "SELECT count(*) FROM %s.files" % t),
      "snapshots": one(s, "SELECT count(*) FROM %s.snapshots" % t),
      "location": [r[1] for r in s.sql("DESCRIBE TABLE EXTENDED %s" % t).collect() if r[0] == "Location"][0]})
