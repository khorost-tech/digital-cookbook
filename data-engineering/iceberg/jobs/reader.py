"""Ось 2: читатель во время записи — 4 файла новых строк, чтение после 2-го.

Новые строки — заказы 08.09 (4 части по четверти). Папка: каждая часть — отдельный
parquet-файл, читатель читает каталог после второго. Iceberg: все четыре части
пишутся в одной транзакции PyIceberg (файлы данных появляются в хранилище при
каждом append), читатель загружает таблицу заново после второго — коммита ещё нет.
Контроль — чтение после окончания записи.

    python -m jobs.reader folder | iceberg
"""

import sys

import pyarrow as pa

from generator import gen
from jobs import lake

BASE, NEW = "2026-09-07", "2026-09-08"


def parts(rows, n=4):
    k = (len(rows) + n - 1) // n
    return [rows[i:i + k] for i in range(0, len(rows), k)]


def folder():
    d = gen.by_day(gen.orders())
    lake.clear("folder/reader")
    lake.write_file("folder/reader/base.parquet", d[BASE])
    seen_mid = written_mid = None
    for i, p in enumerate(parts(d[NEW]), 1):
        lake.write_file("folder/reader/new-%d.parquet" % i, p)
        if i == 2:
            seen_mid = lake.read_folder("folder/reader")[0].num_rows - len(d[BASE])
            written_mid = lake.objects("folder/reader") - 1
    seen_end = lake.read_folder("folder/reader")[0].num_rows - len(d[BASE])
    return seen_mid, seen_end, len(d[NEW]), written_mid


def iceberg():
    d = gen.by_day(gen.orders())
    cat = lake.catalog()
    cat.create_namespace_if_not_exists("shop")
    name = "shop.reader"
    if cat.table_exists(name):
        cat.purge_table(name)
    t = cat.create_table(name, schema=lake.SCHEMA)
    t.append(lake.table(d[BASE]))
    data_prefix = t.location().replace("s3://%s/" % lake.BUCKET, "") + "/data"
    before = lake.objects(data_prefix)
    seen_mid = written_mid = None
    with t.transaction() as tx:
        for i, p in enumerate(parts(d[NEW]), 1):
            tx.append(pa.Table.from_pylist(p, schema=lake.SCHEMA))
            if i == 2:
                seen_mid = cat.load_table(name).scan().to_arrow().num_rows - len(d[BASE])
                written_mid = lake.objects(data_prefix) - before
    seen_end = cat.load_table(name).scan().to_arrow().num_rows - len(d[BASE])
    return seen_mid, seen_end, len(d[NEW]), written_mid


def main():
    seen_mid, seen_end, total, files = (folder if sys.argv[1] == "folder" else iceberg)()
    lake.emit({"new_rows": total, "seen_mid": seen_mid, "seen_end": seen_end, "files_in_storage_mid": files})


if __name__ == "__main__":
    main()
