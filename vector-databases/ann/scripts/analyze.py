"""Разбор осей стенда vector-databases/ann: контроль каждой оси первым.

    python3 scripts/analyze.py hnsw   → разбор оси, код 0 — контроль сошёлся
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.common import read  # noqa: E402


def check(axis, by):
    for v, r in by.items():
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (v, r["detail"]))
    if axis == "brute" and by["control"]["gt_agree"] != by["control"]["queries"]:
        raise SystemExit("контроль: точный перебор разошёлся с истиной датасета")
    if axis == "ivf" and not (by["control"]["recall_at_10"] == 1 and by["control"]["scanned_all"]):
        raise SystemExit("контроль: nprobe = nlist не дал recall 1")
    if axis == "hnsw" and not by["m16-efc200"]["recall_monotonic"]:
        raise SystemExit("контроль: recall HNSW убывает с ef")


def main():
    axis = sys.argv[1]
    by = read(axis)
    check(axis, by)
    for v, r in by.items():
        print("%-14s %-9s %s" % (v, r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
