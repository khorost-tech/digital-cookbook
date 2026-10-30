"""Ось 6: эволюция партиций и скрытое партиционирование (jobs/sp_partitions.py, jobs/plan.py).

    python3 scripts/run-partitions.py              → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, PARTITIONS  # noqa: E402


def measure(name):
    if name == "iceberg":
        evo = stack.spark("sp_partitions")
        plan = stack.tools("jobs.plan", "iceberg")
        r = {"rows_total": evo["rows"], "files_spec": evo["files_spec"], "old_files": evo["old_files"],
             "old_files_kept": evo["old_files_kept"], **plan}
    else:
        r = stack.tools("jobs.plan", "folder")
    r["probe"] = "filter created_at on 2026-09-03"
    r["outcome"] = "match" if r["files_planned"] == 1 and r["rows"] > 0 else "mismatch"
    r["detail"] = "фильтр по времени заказа: файлов в плане %d из %d, строк %d" % (
        r["files_planned"], r["files_total"], r["rows"])
    return r


if __name__ == "__main__":
    common.run("partitions", measure, ("folder", "iceberg"), PARTITIONS, END_MARKER)
