"""Ось 3 (Iceberg): жизненный цикл удаления строки — что остаётся в хранилище.

Удалённая строка исчезает из текущего снапшота, но физически остаётся в
файлах, пока на них ссылается хоть один снапшот (copy-on-write) или пока файл
удалений не применён к данным (merge-on-read). Фазы над одной таблицей:

    spark-submit jobs/sp_erase.py MODE delete    создать, записать 14 дней, удалить заказ
    spark-submit jobs/sp_erase.py MODE expire    expire_snapshots: оставить последний снапшот
    spark-submit jobs/sp_erase.py MODE compact   rewrite_data_files с применением удалений
MODE: copy-on-write | merge-on-read. Физические копии считает раннер (jobs/lake.py).
"""

import sys

from generator import gen
from jobs.sp_common import emit, fresh, one, orders_df
from jobs.spark_session import session

mode, phase = sys.argv[1], sys.argv[2]
name = "erase_%s" % mode.replace("-", "_")
t = "lake.shop." + name
s = session("erase-%s-%s" % (mode, phase))
if phase == "delete":
    fresh(s, name, "TBLPROPERTIES ('write.delete.mode'='%s', 'format-version'='2')" % mode)
    orders_df(s).writeTo(t).append()
# жертва — по генератору: первый заказ 03.09, одинаковый во всех фазах
victim = min(r["id"] for r in gen.orders() if r["created_at"].date().isoformat() == "2026-09-03")
if phase == "delete":
    s.sql("DELETE FROM %s WHERE id = %d" % (t, victim))
elif phase == "expire":
    ts = one(s, "SELECT max(committed_at) FROM %s.snapshots" % t)
    s.sql("CALL lake.system.expire_snapshots(table => 'shop.%s', older_than => TIMESTAMP '%s', retain_last => 1)"
          % (name, ts))
elif phase == "compact":
    s.sql("CALL lake.system.rewrite_data_files(table => 'shop.%s', where => \"to_date(created_at) = '2026-09-03'\", "
          "options => map('rewrite-all', 'true'))" % name)
first = one(s, "SELECT snapshot_id FROM %s.snapshots ORDER BY committed_at LIMIT 1" % t)
emit({"mode": mode, "phase": phase, "victim": victim,
      "current": one(s, "SELECT count(*) FROM %s WHERE id = %d" % (t, victim)),
      "oldest_snapshot": one(s, "SELECT count(*) FROM %s VERSION AS OF %d WHERE id = %d" % (t, first, victim)),
      "snapshots": one(s, "SELECT count(*) FROM %s.snapshots" % t),
      "delete_files": one(s, "SELECT count(*) FROM %s.files WHERE content > 0" % t)})
