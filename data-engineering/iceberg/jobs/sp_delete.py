"""Оси 3–4 (Iceberg): удаление одного заказа и time travel.

Таблица, партиционированная по дню, 14 дней заказов. Удаляется один заказ за
03.09 в режиме copy-on-write или merge-on-read. Счёт по сводке снапшота: файлов
данных удалено и добавлено, файлов удалений добавлено, строк в переписанных
файлах. Затем — изменение статуса заказов дня и чтение версии до изменений;
откат к первому снапшоту.

    spark-submit jobs/sp_delete.py copy-on-write | merge-on-read
"""

import sys

from jobs.sp_common import emit, fresh, one, orders_df
from jobs.spark_session import session

mode = sys.argv[1]
s = session("delete-" + mode)
t = "lake.shop.del_%s" % mode.replace("-", "_")
fresh(s, t.split(".")[-1], "TBLPROPERTIES ('write.delete.mode'='%s', 'format-version'='2')" % mode)
orders_df(s).writeTo(t).append()
first = one(s, "SELECT snapshot_id FROM %s.snapshots ORDER BY committed_at LIMIT 1" % t)
total = one(s, "SELECT count(*) FROM %s" % t)
victim = one(s, "SELECT min(id) FROM %s WHERE to_date(created_at) = '2026-09-03'" % t)
day_rows = one(s, "SELECT count(*) FROM %s WHERE to_date(created_at) = '2026-09-03'" % t)
s.sql("DELETE FROM %s WHERE id = %d" % (t, victim))
summary = s.sql("SELECT summary FROM %s.snapshots ORDER BY committed_at DESC LIMIT 1" % t).collect()[0][0]
after_delete = one(s, "SELECT count(*) FROM %s" % t)
s.sql("UPDATE %s SET status = 'refunded' WHERE to_date(created_at) = '2026-09-04'" % t)
refunded_now = one(s, "SELECT count(*) FROM %s WHERE status = 'refunded'" % t)
asof_rows = one(s, "SELECT count(*) FROM %s VERSION AS OF %d" % (t, first))
asof_refunded = one(s, "SELECT count(*) FROM %s VERSION AS OF %d WHERE status = 'refunded'" % (t, first))
snapshots = one(s, "SELECT count(*) FROM %s.snapshots" % t)
s.sql("CALL lake.system.rollback_to_snapshot('%s', %d)" % (t.replace("lake.", ""), first))
after_rollback = one(s, "SELECT count(*) FROM %s" % t)
rollback_refunded = one(s, "SELECT count(*) FROM %s WHERE status = 'refunded'" % t)


def n(k):
    return int(summary.get(k, 0))


emit({"mode": mode, "rows": total, "day_rows": day_rows, "after_delete": after_delete,
      "deleted_data_files": n("deleted-data-files"), "added_data_files": n("added-data-files"),
      "added_delete_files": n("added-delete-files"), "added_position_deletes": n("added-position-deletes"),
      "rewritten_rows": n("added-records"), "refunded_now": refunded_now, "asof_rows": asof_rows,
      "asof_refunded": asof_refunded, "snapshots": snapshots, "after_rollback": after_rollback,
      "rollback_refunded": rollback_refunded})
