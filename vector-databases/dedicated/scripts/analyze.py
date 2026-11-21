"""Разбор осей стенда vector-databases/dedicated: контроль первым.

    python3 scripts/analyze.py qdrant   → разбор оси, код 0 — контроль сошёлся
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.common import read  # noqa: E402


def check(axis, by):
    for v, r in by.items():
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (v, r["detail"]))
    if axis == "qdrant" and not by["exact"]["only_near_ties"]:
        raise SystemExit("контроль: точный поиск Qdrant разошёлся с истиной не на почти равных скорах")
    if by["load"]["rows"] != 522931:
        raise SystemExit("контроль: загружен не весь корпус")


def main():
    axis = sys.argv[1]
    by = read(axis)
    check(axis, by)
    for v, r in by.items():
        print("%-14s %-9s %s" % (v, r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
