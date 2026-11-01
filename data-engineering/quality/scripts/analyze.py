"""Разбор осей стенда quality: контроль каждой оси первым.

    python3 scripts/analyze.py detect   → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("detect", "reaction", "contract")


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "detect":
        c = by["clean"]
        if c["dbt_caught"] or c["ge_caught"]:
            raise SystemExit("контроль: на чистой партии упали проверки")
        for r in rows:
            if r["variant"] != "clean" and r["bad"] == 0:
                raise SystemExit("%s: дефект не подсажен" % r["variant"])
    if axis == "reaction":
        for v in ("clean-fail", "clean-quarantine"):
            if by[v]["outcome"] != "match":
                raise SystemExit("контроль %s: чистая партия не дала полную витрину" % v)
        f = by["split-fixed"]
        if f["to_mart"] + f["to_quarantine"] != f["rows"]:
            raise SystemExit("контроль: валидные + карантин не равны входным строкам")
        q = by["quarantine"]
        if q["quarantined"] != q["bad"]:
            raise SystemExit("карантин не равен плохим строкам по эталону")
    if axis == "contract":
        c = by["control"]
        if not (c["contracted_built"] and c["uncontracted_built"] and c["downstream_built"]):
            raise SystemExit("контроль: исходная схема не строится")


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-16s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
