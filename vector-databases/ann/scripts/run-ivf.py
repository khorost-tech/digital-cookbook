"""Ось ivf: своя IVF-Flat на полном корпусе.

    control     nlist = √N, nprobe = nlist — просмотрен весь корпус, recall 1
    nlist723    nlist = √N, кривая recall от nprobe
    nlist2892   nlist = 4√N
    skewed723   nlist = √N, центроиды обучены на 10% корпуса вокруг одного документа
    compare     сравнение строк выше

    python3 scripts/run-ivf.py          → fixtures/ivf.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, read, monotonic, outcome, run, tilde  # noqa: E402

I = EXPECTED["ivf"]
NPROBE = "1,2,4,8,16,32,64,128"
DONE = {}


def done():
    """Строки этого запуска; сравнение отдельной командой — по записанной фикстуре."""
    return DONE or read("ivf")


def ivf(name, nlist, train, extra=""):
    args = ("ivf", "-nlist", str(nlist), "-nprobe", NPROBE + extra, "-train", train)
    b = stack.bench(*args)
    return b, args


def row(name, b, args, pts):
    recalls = [p["recall_at_10"] for p in pts]
    r = {"variant": name, "probe": "IVF-Flat nlist = %d, обучение: %s (%d векторов), 20 итераций k-means" % (
        b["nlist"], "каждый 10-й" if b["train"] == "sample" else "10% вокруг одного документа", b["train_n"]),
         "tool_cmd": stack.command(*args), "nlist": b["nlist"], "train": b["train"], "n": b["n"],
         "build_s~": b["build_s"], "list_min~": b["list_min"], "list_median~": b["list_median"],
         "list_max~": b["list_max"], "empty_lists~": b["empty_lists"],
         "points": tilde(pts, ("recall_at_10", "p50_us", "p99_us", "dists_per_query")),
         "recall_monotonic": monotonic(recalls, 0), "outcome": outcome(monotonic(recalls, 0)),
         "detail": "списки %d…%d (медиана %d); recall@10 %s при nprobe %s" % (
             b["list_min"], b["list_max"], b["list_median"], " / ".join("%.3f" % x for x in recalls),
             "/".join(str(p["param"]) for p in pts))}
    DONE[name] = r
    return r


def first_reaching(r, target):
    """Сколько векторов на запрос просмотрено в первой точке с recall ≥ target."""
    p = next((p for p in r["points"] if p["recall_at_10~"] >= target), None)
    return (p["param"], p["dists_per_query~"]) if p else (None, None)


def measure(name):
    if name == "nlist723":
        n = I["nlist_sqrt"]
        b, args = ivf(name, n, "sample", ",%d" % n)
        full = b["points"][-1]
        ctl = {"variant": "control", "probe": "IVF-Flat nlist = nprobe = %d: просмотр всего корпуса" % n,
               "tool_cmd": stack.command(*args), "recall_at_10": full["recall_at_10"],
               "scanned_all": full["dists_per_query"] == b["n"] + n,
               "outcome": outcome(full["recall_at_10"] == 1 and full["dists_per_query"] == b["n"] + n),
               "detail": "recall@10 %.4f, просмотрено %.0f векторов на запрос (корпус + %d центроидов)" % (
                   full["recall_at_10"], full["dists_per_query"], n)}
        return [ctl, row(name, b, args, b["points"][:-1])]
    if name == "nlist2892":
        b, args = ivf(name, I["nlist_4sqrt"], "sample", ",256")
        return row(name, b, args, b["points"])
    if name == "skewed723":
        b, args = ivf(name, I["nlist_sqrt"], "skewed")
        return row(name, b, args, b["points"])
    if name == "compare":
        d = done()
        a, c, s = d["nlist723"], d["nlist2892"], d["skewed723"]
        pa, da = first_reaching(a, 0.9)
        pc, dc = first_reaching(c, 0.9)
        finer_cheaper = da is not None and dc is not None and dc < da
        skew_ratio = (s["list_max~"] / s["list_median~"]) / (a["list_max~"] / a["list_median~"])
        skew_costs = all(ps["dists_per_query~"] > pa_["dists_per_query~"]
                         for ps, pa_ in zip(s["points"], a["points"]))
        ok = finer_cheaper and skew_ratio > 2 and skew_costs
        return {"probe": "сравнение строк выше", "tool_cmd": "—", "finer_cheaper_at_090": finer_cheaper,
                "skewed_lists_uneven": skew_ratio > 2, "skewed_scans_more": skew_costs,
                "skew_ratio~": round(skew_ratio, 1), "outcome": outcome(ok),
                "detail": "recall 0,9: nlist %d — nprobe %s, %s векторов на запрос; nlist %d — nprobe %s, %s; "
                          "перекос max/медиана у нерепрезентативного обучения в %.1f раза больше" % (
                              a["nlist"], pa, da, c["nlist"], pc, dc, skew_ratio)}
    raise KeyError(name)


if __name__ == "__main__":
    run("ivf", measure, ["nlist723", "nlist2892", "skewed723", "compare"])
