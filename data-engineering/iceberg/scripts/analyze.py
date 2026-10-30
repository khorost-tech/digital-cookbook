"""Разбор осей стенда iceberg: контроль каждой оси первым.

    python3 scripts/analyze.py writers   → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("writers", "reader", "delete", "erase", "schema", "partitions", "maintenance")


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "writers":
        for v in ("folder-single", "iceberg-single"):  # один писатель — его изменения на месте
            if by[v]["lost_a"]:
                raise SystemExit("контроль %s: изменения одного писателя потеряны" % v)
        if by["iceberg"]["b_attempts"] < 1:
            raise SystemExit("второй писатель Iceberg не пытался писать")
    if axis == "reader":
        for r in rows:  # после записи видно всё
            if r["seen_end"] != r["new_rows"]:
                raise SystemExit("контроль %s: после записи видно не всё" % r["variant"])
        if by["iceberg"]["files_in_storage_mid"] < 1:
            raise SystemExit("файлы Iceberg не лежали в хранилище до коммита — опыт не различает")
    if axis == "erase":
        # контроль: DELETE убрал строку из текущего снапшота, старейший снапшот её ещё видит
        for r in rows:
            first = r["steps"][0]
            if first["current"] != 0 or first["oldest_snapshot"] != 1 or first["physical"] < 1:
                raise SystemExit("контроль %s: DELETE или time travel не как ожидалось" % r["variant"])
    if axis == "delete":
        for r in rows:
            if r["after_delete"] != r["rows"] - 1:
                raise SystemExit("контроль %s: удалено не одна строка" % r["variant"])
    if axis == "schema":
        pass  # контроль — до изменения схемы все значения на месте (Iceberg: control_values)
        if by["iceberg"]["control_values"] != by["iceberg"]["old_rows"]:
            raise SystemExit("контроль: до изменения схемы значения не все")
    if axis == "partitions":
        if by["folder"]["files_planned_by_dir"] != 1:
            raise SystemExit("контроль: фильтр по колонке каталога в папке не отсёк файлы")
        if by["iceberg"]["old_files_kept"] != by["iceberg"]["old_files"]:
            raise SystemExit("старые файлы переписаны при смене партиций")
    if axis == "maintenance":
        if len({r["rows"] for r in rows}) != 1:
            raise SystemExit("контроль: обслуживание изменило число строк")


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-16s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
