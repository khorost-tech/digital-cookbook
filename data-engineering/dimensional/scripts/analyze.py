"""Разбор осей стенда dimensional: контроль каждой оси первым.

    python3 scripts/analyze.py scd       → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# ось → варианты-контроли, обязанные совпасть с эталоном
CONTROLS = {"scd": ["asof-scd2"], "grain": ["category-item", "orders-distinct"],
            "additivity": ["stock-last", "stock-avg", "cancel-ratio"], "keys": ["late-reassign", "recycled-scd2"]}


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    for v in CONTROLS[axis]:
        if by[v]["outcome"] != "match":
            raise SystemExit("контроль %s/%s не совпал с эталоном" % (axis, v))
    if axis == "keys":
        for v in ("late-inner", "late-unknown", "late-reassign"):
            if by[v]["got_total"] + by[v]["missing"] != by[v]["fact_total"]:
                raise SystemExit("%s: выручка не сходится с фактом" % v)


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-16s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
