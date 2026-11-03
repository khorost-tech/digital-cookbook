"""Разбор осей стенда ebpf/observability: контроль каждой оси первым.

    python3 scripts/analyze.py traces   → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("traces", "profiles", "cost")


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "traces":
        if by["sdk-routes"]["mismatched_routes"]:
            raise SystemExit("контроль: SDK разошёлся со счётом генератора")
    if axis == "profiles":
        if not by["go126"]["within_tolerance"]:
            raise SystemExit("контроль: профиль Go 1.26 вне допуска")
    if axis == "cost":
        if by["load"]["requests"] <= 0:
            raise SystemExit("контроль: нагрузки не было")


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-18s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
