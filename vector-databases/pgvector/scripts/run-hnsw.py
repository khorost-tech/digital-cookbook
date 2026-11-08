"""Ось hnsw: HNSW pgvector, m = 16, ef_construction = 200.

    mwm2gb   maintenance_work_mem 2 ГБ: построение, размер, recall от hnsw.ef_search
    mwm64mb  maintenance_work_mem 64 МБ: граф не помещается в память построения
    compare  сравнение строк выше

    python3 scripts/run-hnsw.py          → fixtures/hnsw.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, monotonic, outcome, read, run, tilde  # noqa: E402

H = EXPECTED["hnsw"]
EFS = "10,20,40,80,160,400"
VECTOR_BYTES = 522931 * 384 * 4
DONE = {}


def done():
    return DONE or read("hnsw")


def at(row, ef):
    return next(p for p in row["points"] if p["param"] == ef)


def measure(name):
    if name in ("mwm2gb", "mwm64mb"):
        mwm = "2GB" if name == "mwm2gb" else "64MB"
        ix = stack.vbench("index", "-kind", "hnsw", "-m", "16", "-efc", "200", "-mwm", mwm)
        efs = EFS if name == "mwm2gb" else "40"
        s = stack.vbench("search", "-param", "hnsw.ef_search", "-values", efs)
        rec = [p["recall_at_10"] for p in s["points"]]
        spilled = "graph_spilled_after" in ix
        r = {"probe": "CREATE INDEX … USING hnsw (embedding vector_ip_ops) WITH (m = 16, ef_construction = 200), "
                      "maintenance_work_mem = %s, max_parallel_maintenance_workers = 4" % mwm,
             "tool_cmd": stack.command("index", "-kind", "hnsw", "-mwm", mwm) + "; " +
                         stack.command("search", "-param", "hnsw.ef_search", "-values", efs),
             "maintenance_work_mem": mwm, "build_s~": ix["build_s"], "index_bytes~": ix["index_bytes"],
             "table_bytes~": ix["table_bytes"], "spilled": spilled, "spilled_after~": ix.get("graph_spilled_after"),
             "notice~": ix.get("notice"), "plan": s["plan"], "uses_index": "docs_hnsw" in s["plan"],
             "points": tilde(s["points"], ("recall_at_10", "p50_us", "p99_us")), "recall_monotonic": monotonic(rec)}
        if name == "mwm2gb":
            r40 = at(r, 40)["recall_at_10~"]
            near = abs(r40 - H["ann_reference_at_40"]) <= H["tolerance"]
            holds_vectors = ix["index_bytes"] >= VECTOR_BYTES
            ok = not spilled and monotonic(rec) and near and holds_vectors and r["uses_index"]
            r.update(near_ann=near, index_holds_vectors=holds_vectors, outcome=outcome(ok),
                     detail="построение %.0f с, индекс %d МБ; recall@10 %s при ef_search %s" % (
                         ix["build_s"], ix["index_bytes"] >> 20, " / ".join("%.3f" % x for x in rec),
                         EFS.replace(",", "/")))
        else:
            r.update(outcome=outcome(spilled),
                     detail="построение %.0f с; %s; recall@10 %.3f при ef_search 40" % (
                         ix["build_s"], ix.get("notice", "без предупреждения"), rec[0]))
        DONE[name] = r
        return r
    if name == "compare":
        d = done()
        a, b = d["mwm2gb"], d["mwm64mb"]
        slow = b["build_s~"] / a["build_s~"]
        same = abs(at(a, 40)["recall_at_10~"] - at(b, 40)["recall_at_10~"]) <= 0.01
        ok = slow >= H["spill_slowdown_min"] and same
        return {"probe": "maintenance_work_mem 64 МБ против 2 ГБ", "tool_cmd": "—", "spill_slower": slow >= H["spill_slowdown_min"],
                "slowdown~": round(slow, 1), "same_recall": same, "outcome": outcome(ok),
                "detail": "построение %.0f с против %.0f с (в %.1f раза дольше), recall@10 при ef_search 40: %.3f против %.3f" % (
                    b["build_s~"], a["build_s~"], slow, at(b, 40)["recall_at_10~"], at(a, 40)["recall_at_10~"])}
    raise KeyError(name)


if __name__ == "__main__":
    run("hnsw", measure, ["mwm2gb", "mwm64mb", "compare"])
