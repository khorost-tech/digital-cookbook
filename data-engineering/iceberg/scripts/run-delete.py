"""Оси 3–4: удаление одного заказа и история (jobs/folder_ops.py, jobs/sp_delete.py).

    python3 scripts/run-delete.py                  → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import DELETE, END_MARKER  # noqa: E402


def measure(name):
    if name == "folder":
        r = stack.tools("jobs.folder_ops", "delete")
        r.update({"added_delete_files": 0, "history": False})
    else:
        r = stack.spark("sp_delete", name)
        r["rewritten_files"] = r["deleted_data_files"]
        r["history"] = r["asof_rows"] == r["rows"] and r["after_rollback"] == r["rows"]
    r["probe"] = "delete 1 order of 2026-09-03"
    r["outcome"] = "match" if r["after_delete"] == r["rows"] - 1 else "mismatch"
    r["detail"] = "строк после удаления %d из %d; переписано файлов %d (строк %d), файлов удалений %d; история %s" % (
        r["after_delete"], r["rows"], r["rewritten_files"], r["rewritten_rows"], r["added_delete_files"],
        "есть" if r["history"] else "нет")
    return r


if __name__ == "__main__":
    common.run("delete", measure, ("folder", "copy-on-write", "merge-on-read"), DELETE, END_MARKER)
