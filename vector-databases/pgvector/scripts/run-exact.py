"""Ось exact: точный перебор в SQL против истины датасета (контроль стенда).

    python3 scripts/run-exact.py          → fixtures/exact.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import outcome, run  # noqa: E402


def measure(_):
    stack.vbench("index", "-kind", "none")
    e = stack.vbench("exact")
    ok = e["gt_agree"] + e["near_tie"] == e["queries"]
    return {"variant": "control", "probe": "ORDER BY embedding <#> $1, id LIMIT 10 при enable_indexscan = off",
            "tool_cmd": stack.command("exact"), "queries": e["queries"], "gt_agree": e["gt_agree"],
            "near_tie": e["near_tie"], "only_near_ties": ok, "p50_us~": e["p50_us"], "plan": e["plan"],
            "outcome": outcome(ok),
            "detail": "выдача совпала с истиной на %d запросах из %d, ещё %d — почти равные скоры на границе десятки; "
                      "%d мс на запрос" % (e["gt_agree"], e["queries"], e["near_tie"], round(e["p50_us"] / 1000))}


if __name__ == "__main__":
    run("exact", measure, ["control"])
