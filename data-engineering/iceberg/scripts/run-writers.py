"""Ось 1: два писателя меняют разные строки одного дня (jobs/writers.py).

    python3 scripts/run-writers.py                 → все варианты, фикстура
    python3 scripts/run-writers.py iceberg         → один вариант
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, WRITERS  # noqa: E402

VARIANTS = ("folder-single", "folder", "iceberg-single", "iceberg")


def measure(name):
    r = stack.tools("jobs.writers", name)
    r["probe"] = "day 2026-09-03"
    lost_a = r["a_want"] - r["a_kept"]
    lost_b = r["b_want"] - r["b_kept"]
    r["lost_a"], r["lost_b"] = lost_a, lost_b
    r["outcome"] = "match" if lost_a == 0 and lost_b == 0 and r["rows"] == r["want_rows"] else "mismatch"
    r["detail"] = "изменений A на месте %d из %d, B %d из %d; коммитов B отклонено %d" % (
        r["a_kept"], r["a_want"], r["b_kept"], r["b_want"], r["b_rejected"])
    return r


if __name__ == "__main__":
    common.run("writers", measure, VARIANTS, WRITERS, END_MARKER)
