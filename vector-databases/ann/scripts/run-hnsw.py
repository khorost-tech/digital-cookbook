"""Ось hnsw: своя HNSW на полном корпусе, сетка M / ef_construction / ef_search.

    m16-efc200, m8-efc200, m32-efc200, m16-efc100 — построение и кривая recall от ef
    compare — сравнение конфигураций (строится по четырём строкам выше)

    python3 scripts/run-hnsw.py          → fixtures/hnsw.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, read, monotonic, outcome, run, tilde  # noqa: E402

EFS = "10,20,40,80,160,400"
CONFIGS = {"m16-efc200": (16, 200), "m8-efc200": (8, 200), "m32-efc200": (32, 200), "m16-efc100": (16, 100)}
H = EXPECTED["hnsw"]
DONE = {}


def done():
    """Строки этого запуска; сравнение отдельной командой — по записанной фикстуре."""
    return DONE or read("hnsw")


def recall_at(row, ef):
    return next(p["recall_at_10~"] for p in row["points"] if p["param"] == ef)


def build(name):
    m, efc = CONFIGS[name]
    args = ("hnsw", "-m", str(m), "-efc", str(efc), "-ef", EFS)
    b = stack.bench(*args)
    n, levels = b["n"], b["levels"]
    # слой 0 — до 2M связей, верхние — до M
    bound = n * 2 * m * 4 + sum(levels[1:]) * m * 4
    recalls = [p["recall_at_10"] for p in b["points"]]
    target = next((p["param"] for p in b["points"] if p["recall_at_10"] >= H["recall_target"]), None)
    reaches = target is not None and target <= H["ef_target_max"]
    unreach_ok = b["unreachable"] <= H["unreachable_max_frac"] * n
    ok = monotonic(recalls) and b["link_bytes"] <= bound and unreach_ok and (reaches or name != "m16-efc200")
    row = {"probe": "HNSW M = %d, ef_construction = %d, построение на всех ядрах, поиск в один поток" % (m, efc),
           "tool_cmd": stack.command(*args), "m": m, "efc": efc, "n": n, "levels": levels,
           "build_s~": b["build_s"], "link_bytes~": b["link_bytes"], "link_bound": bound,
           "link_within": b["link_bytes"] <= bound, "heap_bytes~": b["heap_bytes"], "vector_bytes": b["vector_bytes"],
           "unreachable~": b["unreachable"], "unreachable_ok": unreach_ok,
           "points": tilde(b["points"], ("recall_at_10", "p50_us", "p99_us", "dists_per_query")),
           "recall_monotonic": monotonic(recalls), "ef_reaching_target~": target, "reaches_target": reaches,
           "outcome": outcome(ok),
           "detail": "построение %.0f с; recall@10 %s при ef %s; 0,95 — с ef %s; недостижимых узлов %d" % (
               b["build_s"], " / ".join("%.3f" % r for r in recalls), EFS.replace(",", "/"), target,
               b["unreachable"])}
    DONE[name] = row
    return row


def compare():
    d = done()
    at40 = [recall_at(d[k], 40) for k in ("m8-efc200", "m16-efc200", "m32-efc200")]
    builds = [d[k]["build_s~"] for k in ("m8-efc200", "m16-efc200", "m32-efc200")]
    m_recall = at40[0] < at40[1] < at40[2]
    m_build = builds[0] < builds[1] < builds[2]
    efc_faster = d["m16-efc100"]["build_s~"] < d["m16-efc200"]["build_s~"]
    efc_not_better = recall_at(d["m16-efc100"], 40) <= recall_at(d["m16-efc200"], 40) + 0.005
    ok = m_recall and m_build and efc_faster and efc_not_better
    return {"variant": "compare", "probe": "сравнение конфигураций по строкам выше", "tool_cmd": "—",
            "recall_grows_with_m": m_recall, "build_grows_with_m": m_build,
            "efc100_builds_faster": efc_faster, "efc100_not_better": efc_not_better, "outcome": outcome(ok),
            "detail": "при ef = 40 recall %.3f / %.3f / %.3f для M = 8 / 16 / 32, построение %.0f / %.0f / %.0f с; "
                      "ef_construction 100: %.0f с и %.3f" % (*at40, *builds, d["m16-efc100"]["build_s~"],
                                                             recall_at(d["m16-efc100"], 40))}


def measure(name):
    if name == "compare":
        return compare()
    return build(name)


if __name__ == "__main__":
    run("hnsw", measure, list(CONFIGS) + ["compare"])
