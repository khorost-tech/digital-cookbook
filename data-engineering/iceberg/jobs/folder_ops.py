"""Оси 3–5 для папки: удаление строки, история, переименование и повторное
добавление колонки — так, как это делают без формата таблицы.

    python -m jobs.folder_ops delete | schema
"""

import sys

import pyarrow as pa

from generator import gen
from jobs import lake

DAY = "2026-09-03"


def delete():
    lake.clear("folder/del")
    d = gen.by_day(gen.orders())
    for day, rows in d.items():
        lake.write_file("folder/del/day=%s.parquet" % day, rows)
    before = lake.read_folder("folder/del")[0].num_rows
    victim = min(r["id"] for r in d[DAY])
    kept = [r for r in d[DAY] if r["id"] != victim]
    lake.write_file("folder/del/day=%s.parquet" % DAY, kept)  # удалить строку = переписать файл
    after = lake.read_folder("folder/del")[0].num_rows
    versions = lake.objects("folder/del/day=%s" % DAY)  # прежней версии файла нет
    lake.emit({"rows": before, "after_delete": after, "rewritten_rows": len(kept), "rewritten_files": 1,
               "versions_of_day_file": versions})


def schema():
    lake.clear("folder/schema")
    d = gen.by_day(gen.orders())
    old = [r for day, rows in d.items() if day <= "2026-09-07" for r in rows]
    lake.write_file("folder/schema/old.parquet", old)
    # переименование status → order_status: новые файлы пишутся с новым именем
    new = [dict({k: v for k, v in r.items() if k != "status"}, order_status=r["status"]) for r in d["2026-09-08"]]
    s_new = pa.schema([f for f in lake.SCHEMA if f.name != "status"] + [pa.field("order_status", pa.string())])
    lake.write_file("folder/schema/new-renamed.parquet", new, s_new)
    t, _ = lake.read_folder("folder/schema", schema=s_new)
    old_ids = {r["id"] for r in old}
    renamed_old_values = sum(1 for r in t.to_pylist() if r["id"] in old_ids and r["order_status"] is not None)
    # «удалили» customer_id и добавили снова: новые файлы — с колонкой заново, старые — со старыми значениями
    t2, _ = lake.read_folder("folder/schema", schema=s_new)
    readded_old_values = sum(1 for r in t2.to_pylist() if r["id"] in old_ids and r["customer_id"] is not None)
    lake.emit({"old_rows": len(old), "renamed_old_values": renamed_old_values,
               "readded_old_values": readded_old_values})


if __name__ == "__main__":
    {"delete": delete, "schema": schema}[sys.argv[1]]()
