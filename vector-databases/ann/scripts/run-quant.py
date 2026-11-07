"""Ось quant: сжатие векторов и пересчёт по исходным.

    sq8     байт на координату, пересчёт 10 / 100 / 1000 кандидатов
    pq48    PQ, 48 подвекторов по 8 координат, 256 центроидов
    pq96    PQ, 96 подвекторов по 4 координаты
    compare сравнение pq48 и pq96

    python3 scripts/run-quant.py          → fixtures/quant.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import EXPECTED, read, outcome, run, tilde  # noqa: E402

Q = EXPECTED["quant"]
DIM = 384
DONE = {}


def done():
    """Строки этого запуска; сравнение отдельной командой — по записанной фикстуре."""
    return DONE or read("quant")


def times(n):
    """раз / раза: «в 32 раза», «в 16 раз»."""
    return "раза" if 2 <= n % 10 <= 4 and not 12 <= n % 100 <= 14 else "раз"


def pick(pts, r, rescore):
    return next(p["recall_at_10"] for p in pts if p["r"] == r and p["rescore"] == rescore)


def measure(name):
    if name == "sq8":
        args = ("sq8", "-r", "10,100,1000")
        b = stack.bench(*args)
        raw, best = pick(b["points"], 10, False), pick(b["points"], 1000, True)
        size_ok = b["code_bytes"] == b["n"] * DIM
        ok = size_ok and raw >= Q["sq8_raw_min"]
        r = {"probe": "SQ8: по координате диапазон [min, max] на 256 уровней", "tool_cmd": stack.command(*args),
             "n": b["n"], "code_bytes": b["code_bytes"], "float_bytes": b["n"] * DIM * 4, "code_size_ok": size_ok,
             "raw_recall~": raw, "raw_ok": raw >= Q["sq8_raw_min"],
             "points": tilde(b["points"], ("recall_at_10", "p50_us", "p99_us")), "outcome": outcome(ok),
             "detail": "коды %d МБ вместо %d МБ; recall@10 %.3f без пересчёта, %.3f с пересчётом 1000" % (
                 b["code_bytes"] >> 20, b["n"] * DIM * 4 >> 20, raw, best)}
    elif name in ("pq48", "pq96"):
        m = int(name[2:])
        args = ("pq", "-pqm", str(m), "-r", "10,100,1000")
        b = stack.bench(*args)
        raw, best = pick(b["points"], 10, False), pick(b["points"], 1000, True)
        size_ok = b["code_bytes"] == b["n"] * m
        # revision_after_session_a: «теряет больше половины» сменилось на «теряет
        # не меньше 40% и отстаёт от SQ8 на 0,3+» — порог 0,5 был взят с FiQA
        rev = Q["revision_after_session_a"]
        sq8_raw = done()["sq8"]["raw_recall~"]
        loses = raw <= rev["raw_max"] and sq8_raw - raw >= rev["gap_to_sq8_min"]
        ok = size_ok and best >= Q["pq_rescored_min"] and (loses or m != 48)
        r = {"probe": "PQ: %d подвекторов по %d координат, 256 центроидов, обучение на каждом 10-м" % (m, b["sub"]),
             "tool_cmd": stack.command(*args), "n": b["n"], "pq_m": m, "code_bytes": b["code_bytes"],
             "codebook_bytes": b["codebook_bytes"], "compression": DIM * 4 // m, "code_size_ok": size_ok,
             "raw_recall~": raw, "rescored_1000~": best, "raw_loses_much": loses,
             "rescored_ok": best >= Q["pq_rescored_min"], "build_s~": b["build_s"],
             "points": tilde(b["points"], ("recall_at_10", "p50_us", "p99_us")), "outcome": outcome(ok),
             "detail": "коды %.1f МБ (сжатие в %d %s); recall@10 %.3f без пересчёта, %.3f / %.3f с пересчётом 100 / 1000" % (
                 b["code_bytes"] / 2**20, DIM * 4 // m, times(DIM * 4 // m), raw, pick(b["points"], 100, True), best)}
    elif name == "compare":
        a, b = done()["pq48"], done()["pq96"]
        ok = b["raw_recall~"] > a["raw_recall~"] and b["rescored_1000~"] >= a["rescored_1000~"]
        return {"probe": "PQ m = 96 против m = 48", "tool_cmd": "—", "finer_more_accurate": ok,
                "outcome": outcome(ok),
                "detail": "без пересчёта %.3f против %.3f; с пересчётом 1000 — %.3f против %.3f" % (
                    b["raw_recall~"], a["raw_recall~"], b["rescored_1000~"], a["rescored_1000~"])}
    else:
        raise KeyError(name)
    DONE[name] = r
    return r


if __name__ == "__main__":
    run("quant", measure, ["sq8", "pq48", "pq96", "compare"])
