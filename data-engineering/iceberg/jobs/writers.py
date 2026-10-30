"""Ось 1: два писателя меняют разные строки одного дня — чередование задано.

Оба читают одно состояние; A меняет статус у заказов с id % 10 == 1, B — у
id % 10 == 2 (оба — на 'refunded'); A пишет первым, B — вторым. Папка: файл дня
перезаписывается целиком. Iceberg (PyIceberg): overwrite дня с фильтром;
коммит B идёт от снапшота, прочитанного до коммита A. PyIceberg повторяет
отклонённый коммит сам и при повторе проверяет конфликт; если B отклонён —
B перечитывает таблицу и повторяет изменение.

    python -m jobs.writers folder | iceberg | folder-single | iceberg-single
"""

import sys

import pyarrow.compute as pc
from pyiceberg.exceptions import CommitFailedException, ValidationException
from pyiceberg.expressions import EqualTo
from pyiceberg.partitioning import PartitionField, PartitionSpec
from pyiceberg.schema import Schema
from pyiceberg.transforms import IdentityTransform
from pyiceberg.types import IntegerType, LongType, NestedField, StringType, TimestampType

from generator import gen
from jobs import lake

DAY = "2026-09-03"


def change(rows, mod):
    return [dict(r, status="refunded") if r["id"] % 10 == mod else r for r in rows]


def count_changed(rows, original, mod):
    want = {r["id"] for r in original if r["id"] % 10 == mod and r["status"] != "refunded"}
    return sum(1 for r in rows if r["id"] in want and r["status"] == "refunded"), len(want)


def folder(single):
    rows = gen.by_day(gen.orders())[DAY]
    path = "folder/writers/day=%s.parquet" % DAY
    lake.clear("folder/writers")
    lake.write_file(path, rows)
    read_a = lake.read_folder("folder/writers")[0].to_pylist()
    read_b = lake.read_folder("folder/writers")[0].to_pylist()
    lake.write_file(path, change(read_a, 1))
    if not single:
        lake.write_file(path, change(read_b, 2))
    final = lake.read_folder("folder/writers")[0].to_pylist()
    return final, rows, {"b_attempts": 0 if single else 1, "b_rejected": 0}


def iceberg(single):
    rows = gen.by_day(gen.orders())[DAY]
    cat = lake.catalog()
    cat.create_namespace_if_not_exists("shop")
    name = "shop.writers"
    if cat.table_exists(name):
        cat.purge_table(name)
    schema = Schema(NestedField(1, "id", LongType(), required=False), NestedField(2, "customer_id", IntegerType()),
                    NestedField(3, "status", StringType()), NestedField(4, "amount", LongType()),
                    NestedField(5, "created_at", TimestampType()), NestedField(6, "day", StringType()))
    spec = PartitionSpec(PartitionField(source_id=6, field_id=1000, transform=IdentityTransform(), name="day"))
    t = cat.create_table(name, schema=schema, partition_spec=spec)
    import pyarrow as pa
    def arrow(rs):
        return pa.Table.from_pylist([dict(r, day=DAY) for r in rs], schema=t.schema().as_arrow())
    t.append(arrow(rows))
    ta, tb = cat.load_table(name), cat.load_table(name)  # оба читают одно состояние
    read_a = ta.scan().to_arrow().drop_columns(["day"]).to_pylist()
    read_b = tb.scan().to_arrow().drop_columns(["day"]).to_pylist()
    ta.overwrite(arrow(change(read_a, 1)), overwrite_filter=EqualTo("day", DAY))
    meta = {"b_attempts": 0, "b_rejected": 0}
    if not single:
        try:
            meta["b_attempts"] += 1
            tb.overwrite(arrow(change(read_b, 2)), overwrite_filter=EqualTo("day", DAY))
        except (CommitFailedException, ValidationException) as e:
            # PyIceberg повторяет коммит сам и при повторе проверяет конфликт:
            # A добавил файлы под тот же фильтр перезаписи — повтор отклонён
            meta["b_rejected"] += 1
            meta["b_error"] = type(e).__name__
            tb = cat.load_table(name)  # перечитать и повторить
            meta["b_attempts"] += 1
            tb.overwrite(arrow(change(tb.scan().to_arrow().drop_columns(["day"]).to_pylist(), 2)),
                         overwrite_filter=EqualTo("day", DAY))
    final = cat.load_table(name).scan().to_arrow().drop_columns(["day"]).to_pylist()
    meta["snapshots"] = len(cat.load_table(name).snapshots())
    return final, rows, meta


def main():
    variant = sys.argv[1]
    single = variant.endswith("-single")
    final, original, meta = (folder if variant.startswith("folder") else iceberg)(single)
    a_kept, a_want = count_changed(final, original, 1)
    b_kept, b_want = count_changed(final, original, 2)
    lake.emit({"rows": len(final), "want_rows": len(original), "a_kept": a_kept, "a_want": a_want,
               "b_kept": b_kept, "b_want": 0 if single else b_want, **meta})


if __name__ == "__main__":
    main()
