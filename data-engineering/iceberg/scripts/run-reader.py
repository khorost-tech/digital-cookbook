"""Ось 2: читатель во время записи 4 файлов (jobs/reader.py).

    python3 scripts/run-reader.py                  → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, READER  # noqa: E402


def measure(name):
    r = stack.tools("jobs.reader", name)
    r["probe"] = "4 parts of 2026-09-08"
    r["outcome"] = "match" if r["seen_mid"] in (0, r["new_rows"]) and r["seen_end"] == r["new_rows"] else "mismatch"
    r["detail"] = "после 2 файлов из 4 видно новых строк %d из %d (файлов уже в хранилище %d); после записи — %d" % (
        r["seen_mid"], r["new_rows"], r["files_in_storage_mid"], r["seen_end"])
    return r


if __name__ == "__main__":
    common.run("reader", measure, ("folder", "iceberg"), READER, END_MARKER)
