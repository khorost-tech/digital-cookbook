"""Ось ivfflat: IVFFlat pgvector, lists = 723, recall от ivfflat.probes.

    python3 scripts/run-ivfflat.py          → fixtures/ivfflat.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, monotonic, outcome, run, tilde  # noqa: E402

I = EXPECTED["ivfflat"]
PROBES = "1,2,4,8,16,32,64,128"


def measure(_):
    ix = stack.vbench("index", "-kind", "ivfflat", "-lists", "723")
    s = stack.vbench("search", "-param", "ivfflat.probes", "-values", PROBES)
    rec = {p["param"]: p["recall_at_10"] for p in s["points"]}
    gaps = {k: round(rec[int(k)] - v, 4) for k, v in I["ann_reference"].items()}
    near = all(abs(g) <= I["tolerance"] for g in gaps.values())
    ok = monotonic(list(rec.values()), 0) and near and "docs_ivf" in s["plan"]
    return {"variant": "lists723", "probe": "CREATE INDEX … USING ivfflat (embedding vector_ip_ops) WITH (lists = 723)",
            "tool_cmd": stack.command("index", "-kind", "ivfflat", "-lists", "723") + "; " +
                        stack.command("search", "-param", "ivfflat.probes", "-values", PROBES),
            "build_s~": ix["build_s"], "index_bytes~": ix["index_bytes"], "plan": s["plan"],
            "points": tilde(s["points"], ("recall_at_10", "p50_us", "p99_us")), "recall_monotonic": monotonic(list(rec.values()), 0),
            "gap_to_ann~": gaps, "near_ann": near, "outcome": outcome(ok),
            "detail": "построение %.0f с, индекс %d МБ; recall@10 %s при probes %s" % (
                ix["build_s"], ix["index_bytes"] >> 20, " / ".join("%.3f" % x for x in rec.values()), PROBES.replace(",", "/"))}


if __name__ == "__main__":
    run("ivfflat", measure, ["lists723"])
