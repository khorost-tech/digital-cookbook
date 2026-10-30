"""Ось 5: переименование колонки и повторное добавление колонки с тем же именем.

    python3 scripts/run-schema.py                  → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, SCHEMA  # noqa: E402


def measure(name):
    r = stack.tools("jobs.folder_ops", "schema") if name == "folder" else stack.spark("sp_schema")
    r["probe"] = "old rows 01-07.09"
    # верно: после переименования значения на месте; после повторного добавления — пусто
    ok = r["renamed_old_values"] == r["old_rows"] and r["readded_old_values"] == 0
    r["outcome"] = "match" if ok else "mismatch"
    r["detail"] = "у %d старых строк: значений под новым именем %d; «воскресших» значений %d" % (
        r["old_rows"], r["renamed_old_values"], r["readded_old_values"])
    return r


if __name__ == "__main__":
    common.run("schema", measure, ("folder", "iceberg"), SCHEMA, END_MARKER)
