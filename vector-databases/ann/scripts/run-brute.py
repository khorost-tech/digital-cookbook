"""Ось brute: точный перебор от размера корпуса и контроль против истины.

    control   полный корпус: выдача = истина датасета на 200 запросах
    scaling   N = 10 тыс. … полный корпус: время на запрос линейно по N

    python3 scripts/run-brute.py          → fixtures/brute.jsonl
"""

import os
import statistics
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, outcome, run  # noqa: E402

NS = "10000,50000,100000,250000,522931"
ARGS = ("brute", "-n", NS, "-q", "200")


def measure(_):
    b = stack.bench(*ARGS)
    pts = b["points"]
    full = pts[-1]
    ok_control = full["gt_agree"] == full["queries"]
    ratio = [p["p50_us"] / p["n"] * 1000 for p in pts if p["n"] >= 50000]
    med = statistics.median(ratio)
    tol = EXPECTED["brute"]["linear_tolerance"]
    linear = all(abs(r / med - 1) <= tol for r in ratio)
    cmd = stack.command(*ARGS)
    return [
        {"variant": "control", "probe": "точный перебор полного корпуса против истины датасета", "tool_cmd": cmd,
         "n": full["n"], "queries": full["queries"], "gt_agree": full["gt_agree"], "outcome": outcome(ok_control),
         "detail": "выдача совпала с истиной на %d запросах из %d" % (full["gt_agree"], full["queries"])},
        {"variant": "scaling", "probe": "время точного перебора на запрос от размера корпуса", "tool_cmd": cmd,
         "points": [{"n": p["n"], "p50_us~": p["p50_us"], "dists_per_query": p["dists_per_query"]} for p in pts],
         "us_per_1k_docs~": round(med, 2), "linear": linear, "outcome": outcome(linear),
         "detail": "%.0f мкс на 10 тыс. документов … %.0f мс на %d; %.2f мкс на тысячу документов (медиана)" % (
             pts[0]["p50_us"], full["p50_us"] / 1000, full["n"], med)},
    ]


if __name__ == "__main__":
    run("brute", measure, ["all"])
