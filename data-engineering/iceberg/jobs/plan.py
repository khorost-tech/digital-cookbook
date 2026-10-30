"""Ось 6: сколько файлов планирует запрос с фильтром по времени заказа за 03.09.

Iceberg (PyIceberg plan_files по таблице part_t, созданной заданием Spark) и
папка с каталогами-партициями по дню (day=YYYY-MM-DD), где фильтр стоит на
created_at, а не на колонке партиции.

    python -m jobs.plan iceberg | folder
"""

import datetime as dt
import sys

import pyarrow.compute as pc
import pyarrow.dataset as ds
from pyiceberg.expressions import And, GreaterThanOrEqual, LessThan

from generator import gen
from jobs import lake

LO, HI = dt.datetime(2026, 9, 3), dt.datetime(2026, 9, 4)


def iceberg():
    t = lake.catalog().load_table("shop.part_t")
    total = len(list(t.scan().plan_files()))
    # Spark создаёт timestamp как timestamptz Iceberg; сессия Spark — в UTC
    f = And(GreaterThanOrEqual("created_at", LO.isoformat() + "+00:00"), LessThan("created_at", HI.isoformat() + "+00:00"))
    tasks = list(t.scan(row_filter=f).plan_files())
    rows = t.scan(row_filter=f).to_arrow().num_rows
    return total, len(tasks), rows


def folder():
    lake.clear("folder/part")
    for day, rows in gen.by_day(gen.orders()).items():
        lake.write_file("folder/part/day=%s/part.parquet" % day, rows)
    d = ds.dataset("%s/folder/part" % lake.BUCKET, format="parquet", filesystem=lake.s3fs(), partitioning="hive")
    flt = (pc.field("created_at") >= pc.scalar(LO)) & (pc.field("created_at") < pc.scalar(HI))
    planned = len(list(d.get_fragments(filter=flt)))
    by_dir = len(list(d.get_fragments(filter=pc.field("day") == "2026-09-03")))
    rows = d.to_table(filter=flt).num_rows
    return len(list(d.get_fragments())), planned, rows, by_dir


def main():
    if sys.argv[1] == "iceberg":
        total, planned, rows = iceberg()
        lake.emit({"files_total": total, "files_planned": planned, "rows": rows})
    else:
        total, planned, rows, by_dir = folder()
        lake.emit({"files_total": total, "files_planned": planned, "rows": rows, "files_planned_by_dir": by_dir})


if __name__ == "__main__":
    main()
