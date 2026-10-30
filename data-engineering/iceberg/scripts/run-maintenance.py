"""Ось 7: мелкие файлы и обслуживание (jobs/sp_maint.py по фазам).

    python3 scripts/run-maintenance.py             → фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, MAINTENANCE  # noqa: E402

PREFIX = "shop/maint_t"


def measure(name):
    r = stack.spark("sp_maint", name)
    r["probe"] = "50 single-row inserts"
    r["data_objects"] = stack.objects(PREFIX + "/data")
    r["metadata_objects"] = stack.objects(PREFIX + "/metadata")
    r["outcome"] = "match" if r["rows"] == 50 else "mismatch"
    r["detail"] = "строк %d; файлов данных в таблице %d, снапшотов %d; объектов в хранилище: данных %d, метаданных %d" % (
        r["rows"], r["data_files"], r["snapshots"], r["data_objects"], r["metadata_objects"])
    r.pop("location", None)
    return r


if __name__ == "__main__":
    common.run("maintenance", measure, ("write", "compact", "expire"), MAINTENANCE, END_MARKER)
