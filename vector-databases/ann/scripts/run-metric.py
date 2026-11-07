"""Ось metric: cosine, L2 и dot на нормированных векторах и на тех же векторах
со случайной длиной от 0,5 до 2 (первые 100 тыс. документов, 200 запросов).

    python3 scripts/run-metric.py          → fixtures/metric.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, outcome, run  # noqa: E402

M = EXPECTED["metric"]


def measure(_):
    args = ("metric",)
    b = stack.bench(*args)
    nm, sc = b["normalized"], b["scaled"]
    same = min(nm["cosine_vs_l2"]["overlap"], nm["cosine_vs_dot"]["overlap"]) >= M["normalized_min"]
    differ = sc["cosine_vs_dot"]["overlap"] < M["scaled_max"]
    return {"probe": "top-10 по трём метрикам на %d документах, %d запросов" % (b["n"], b["queries"]),
            "tool_cmd": stack.command(*args), "n": b["n"], "queries": b["queries"],
            "normalized~": nm, "scaled~": sc, "normalized_agree": same, "scaled_dot_differs": differ,
            "outcome": outcome(same and differ),
            "detail": "нормированные: пересечение cosine/L2 %.4f, cosine/dot %.4f; с длиной 0,5–2: cosine/L2 %.4f, "
                      "cosine/dot %.4f" % (nm["cosine_vs_l2"]["overlap"], nm["cosine_vs_dot"]["overlap"],
                                           sc["cosine_vs_l2"]["overlap"], sc["cosine_vs_dot"]["overlap"])}


if __name__ == "__main__":
    run("metric", measure, ["all"])
