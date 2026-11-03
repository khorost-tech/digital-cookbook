"""Разбор осей стенда performance/load-testing: контроль каждой оси первым.

    python3 scripts/analyze.py model   → разбор оси, код 0 — контроль сошёлся
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH, MODEL  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("model", "types", "ceiling", "chaos")


def read(axis):
    contract = MODEL if axis == "model" else MATCH
    return contract.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "model":
        bad = [v for v, r in by.items() if v.startswith("control-") and r["outcome"] != "match"]
        if bad:
            raise SystemExit("контроль: без пауз генератор увидел хвост или разошёлся со счётом: %s" % ", ".join(bad))
    if axis == "types":
        if not by["load"]["slo_met"]:
            raise SystemExit("контроль: SLO не выполнен при 50% ёмкости")
    if axis == "ceiling":
        bad = [v for v, r in by.items() if v.endswith("@1000") and not r["holds"]]
        if bad:
            raise SystemExit("контроль: 1000/с не держат: %s" % ", ".join(bad))
    if axis == "chaos":
        for v in ("control-naive", "control-resilient"):
            if not by[v]["hypothesis_held"] or by[v]["errors~"]:
                raise SystemExit("контроль: без сбоя гипотеза не устояла (%s)" % v)


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-24s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
