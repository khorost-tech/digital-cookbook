"""Ось crosscheck: свои реализации против Faiss на тех же параметрах и jvector
(Java) как вторая независимая реализация графа. Свои числа берутся из фикстур
осей hnsw, ivf и quant — эта ось идёт после них.

    faiss-hnsw  M = 16, ef_construction = 200, ef 10 / 40 / 160
    faiss-ivf   nlist = 723, nprobe 1 / 8 / 64
    faiss-pq    m = 48: без пересчёта, пересчёт 100 и 1000
    faiss-sq8   без пересчёта и пересчёт 100
    jvector     M = 16, beam 200, ширина обхода 10 … 400

    python3 scripts/run-crosscheck.py          → fixtures/crosscheck.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, monotonic, outcome, read, run  # noqa: E402

C = EXPECTED["crosscheck"]
TOL = C["tolerance"]


def ours_points(axis, variant, key="param"):
    row = read(axis)[variant]
    return {p[key]: p for p in row["points"]}


def compare(algo, pairs, faiss_out, args):
    """pairs: [(подпись, свой recall, recall Faiss)]."""
    worst = max(abs(a - b) for _, a, b in pairs)
    ok = worst <= TOL[algo]
    return {"probe": "та же конфигурация в Faiss %s" % faiss_out["faiss"],
            "tool_cmd": "faiss/check.py /data/quora " + " ".join(args), "faiss_version": faiss_out["faiss"],
            "pairs~": [{"at": s, "ours": a, "faiss": b} for s, a, b in pairs], "max_gap~": round(worst, 4),
            "tolerance": TOL[algo], "within": ok, "outcome": outcome(ok),
            "detail": "; ".join("%s: %.3f против %.3f" % p for p in pairs) + " (наша / Faiss)"}


def measure(name):
    if name == "faiss-hnsw":
        args = ("hnsw", "16", "200", "10,40,160")
        f = stack.faiss(*args)
        ours = ours_points("hnsw", "m16-efc200")
        fp = {p["param"]: p for p in f["points"]}
        r = compare("hnsw", [("ef %d" % ef, ours[ef]["recall_at_10~"], fp[ef]["recall_at_10"]) for ef in (10, 40, 160)],
                    f, args)
        r["faiss_dists~"] = {str(ef): fp[ef]["dists_per_query"] for ef in (10, 40, 160)}
        r["ours_dists~"] = {str(ef): ours[ef]["dists_per_query~"] for ef in (10, 40, 160)}
        return r
    if name == "faiss-ivf":
        args = ("ivf", "723", "1,8,64")
        f = stack.faiss(*args)
        ours = ours_points("ivf", "nlist723")
        fp = {p["param"]: p for p in f["points"]}
        return compare("ivf", [("nprobe %d" % p, ours[p]["recall_at_10~"], fp[p]["recall_at_10"]) for p in (1, 8, 64)],
                       f, args)
    if name == "faiss-pq":
        args = ("pq", "48", "0,100,1000")
        f = stack.faiss(*args)
        row = read("quant")["pq48"]
        ours = {(p["r"], p["rescore"]): p["recall_at_10~"] for p in row["points"]}
        fp = {p["r"]: p["recall_at_10"] for p in f["points"]}
        return compare("pq", [("без пересчёта", ours[(10, False)], fp[0]), ("пересчёт 100", ours[(100, True)], fp[100]),
                              ("пересчёт 1000", ours[(1000, True)], fp[1000])], f, args)
    if name == "faiss-sq8":
        args = ("sq8",)
        f = stack.faiss(*args)
        row = read("quant")["sq8"]
        ours = {(p["r"], p["rescore"]): p["recall_at_10~"] for p in row["points"]}
        fp = {p["r"]: p["recall_at_10"] for p in f["points"]}
        return compare("sq8", [("без пересчёта", ours[(10, False)], fp[0]), ("пересчёт 100", ours[(100, True)], fp[100])],
                       f, args)
    if name == "jvector":
        args = ("16", "200", "10,20,40,80,160,400")
        j = stack.jvector(*args)
        rec = [p["recall_at_10"] for p in j["points"]]
        ok = monotonic(rec) and rec[-1] >= C["jvector_min"]
        return {"probe": "jvector 4.0.1 (Java 21), M = 16, beam 200, alpha 1,2, с иерархией",
                "tool_cmd": "java vdb.Bench /data/quora " + " ".join(args), "n": j["n"], "build_s~": j["build_s"],
                "points": [{"param": p["param"], "recall_at_10~": p["recall_at_10"],
                            "visited_per_query~": p["visited_per_query"], "p50_us~": p["p50_us"]} for p in j["points"]],
                "recall_monotonic": monotonic(rec), "reaches_min": rec[-1] >= C["jvector_min"], "outcome": outcome(ok),
                "detail": "recall@10 %s при ширине обхода 10/20/40/80/160/400; построение %.0f с" % (
                    " / ".join("%.3f" % x for x in rec), j["build_s"])}
    raise KeyError(name)


if __name__ == "__main__":
    run("crosscheck", measure, ["faiss-hnsw", "faiss-ivf", "faiss-pq", "faiss-sq8", "jvector"])
