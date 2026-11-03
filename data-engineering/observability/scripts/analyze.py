"""Разбор осей стенда observability: контроль каждой оси первым.

    python3 scripts/analyze.py lineage   → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("lineage", "monitors")


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "lineage":
        t = by["tables"]
        if t["datasets"] != t["manifest"]:
            raise SystemExit("контроль: граф Marquez не совпал с манифестом dbt")
    if axis == "monitors":
        # Контроль: детекторы по истории не тревожат в дни без инцидентов.
        for v in ("volume-history", "freshness-history", "distribution-history", "distribution-history-clean"):
            if by[v]["fp_days"]:
                raise SystemExit("контроль %s: тревога без инцидента %s" % (v, by[v]["fp_days"]))
        for r in rows:
            if r["tp"] + r["fn"] != len(r["truth"]) or r["tp"] + r["fp"] != len(r["alerts"]):
                raise SystemExit("%s: счёт не сходится с днями" % r["variant"])


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-22s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
