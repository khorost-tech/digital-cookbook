"""Ось filter: WHERE bucket < b при HNSW (ef_search = 40), с итеративным
сканированием и без, без B-tree по метке и с ним.

    hnsw               только HNSW
    hnsw-btree         HNSW и B-tree по bucket, plan_cache_mode по умолчанию:
                       подготовленный запрос после пяти выполнений идёт по общему плану
    hnsw-btree-custom  то же с plan_cache_mode = force_custom_plan

    python3 scripts/run-filter.py          → fixtures/filter.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import outcome, run  # noqa: E402

SEL = "500,100,10,1"


def pick(points, b, mode):
    return next(p for p in points if p["bucket_lt"] == b and p["mode"] == mode)


def measure(name):
    stack.vbench("index", "-kind", "hnsw", "-m", "16", "-efc", "200", "-reuse")
    if name.startswith("hnsw-btree"):
        stack.vbench("index", "-kind", "btree", "-reuse")
    args = ("filter", "-sel", SEL) + (("-plancache", "force_custom_plan") if name == "hnsw-btree-custom" else ())
    f = stack.vbench(*args)
    pts = [{"bucket_lt": p["bucket_lt"], "selectivity": p["selectivity"], "rows_in_filter": p["rows_in_filter"],
            "mode": p["mode"], "recall_at_10~": p["recall_at_10"], "rows_per_query~": p["rows_per_query"],
            "p50_us~": p["p50_us"], "p99_us~": p["p99_us"], "plan": p["plan"],
            "via_hnsw": "docs_hnsw" in p["plan"]} for p in f["points"]]
    row = {"probe": "SELECT id FROM docs WHERE bucket < b ORDER BY embedding <#> q LIMIT 10; hnsw.ef_search = 40; "
                    "hnsw.iterative_scan off / relaxed_order / strict_order%s" % (
                        {"hnsw": "", "hnsw-btree": "; B-tree по bucket",
                         "hnsw-btree-custom": "; B-tree по bucket; plan_cache_mode = force_custom_plan"}[name]),
           "tool_cmd": stack.command(*args), "plan_cache_mode": f["plan_cache_mode"], "generic_plan": f["generic_plan"],
           "points": pts}
    if name == "hnsw":
        off_full_at_50 = pick(f["points"], 500, "off")["rows_per_query"] >= 9.9
        off_short = all(pick(f["points"], b, "off")["rows_per_query"] < 10 for b in (100, 10, 1))
        iter_full = all(pick(f["points"], b, m)["rows_per_query"] >= 9.99 for b in (500, 100, 10)
                        for m in ("relaxed_order", "strict_order"))
        all_hnsw = all(p["via_hnsw"] for p in pts)
        ok = off_full_at_50 and off_short and iter_full and all_hnsw
        row.update(off_full_at_50=off_full_at_50, off_short_when_selective=off_short, iterative_full=iter_full,
                   always_hnsw=all_hnsw, outcome=outcome(ok))
    elif name == "hnsw-btree":
        # revision_after_session_a: ловушка общего плана подготовленного запроса
        tiny = pick(f["points"], 1, "off")
        custom_btree = "docs_bucket" in tiny["plan"]
        generic_hnsw = "docs_hnsw" in f["generic_plan"]
        generic_wins = tiny["rows_per_query"] < 1
        ok = custom_btree and generic_hnsw and generic_wins
        row.update(custom_plan_btree=custom_btree, generic_plan_hnsw=generic_hnsw, generic_plan_wins=generic_wins,
                   outcome=outcome(ok))
    else:
        tiny = pick(f["points"], 1, "off")
        btree_at_tiny = "docs_bucket" in tiny["plan"] and not tiny_hnsw(tiny)
        exact_at_tiny = tiny["recall_at_10"] == 1
        hnsw_at_half = "docs_hnsw" in pick(f["points"], 500, "off")["plan"]
        ok = btree_at_tiny and exact_at_tiny and hnsw_at_half
        row.update(btree_at_01pct=btree_at_tiny, exact_at_01pct=exact_at_tiny, hnsw_at_50pct=hnsw_at_half,
                   outcome=outcome(ok))
    row["detail"] = "; ".join("%g%%: %s" % (b / 10, " / ".join(
        "%.1f строк, recall %.3f" % (pick(f["points"], b, m)["rows_per_query"], pick(f["points"], b, m)["recall_at_10"])
        for m in ("off", "relaxed_order", "strict_order"))) for b in (500, 100, 10, 1)) + " (off / relaxed / strict)"
    return row


def tiny_hnsw(p):
    return "docs_hnsw" in p["plan"]


if __name__ == "__main__":
    run("filter", measure, ["hnsw", "hnsw-btree", "hnsw-btree-custom"])
