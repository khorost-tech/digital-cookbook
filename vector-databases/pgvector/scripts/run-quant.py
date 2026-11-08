"""Ось quant: HNSW по halfvec(384) и по binary_quantize(embedding) против HNSW
по vector(384) из оси hnsw (эта ось идёт после неё).

    halfvec          индекс по половинной точности, recall при ef_search 40 и 160
    hamming          точный перебор по Хэммингу (Go, без индекса): знак и знак после центрирования
    binary           HNSW по битовым кодам, пересчёт 10 / 100 / 1000 кандидатов
    binary-centered  то же по кодам от векторов минус средний вектор корпуса

    python3 scripts/run-quant.py          → fixtures/quant.jsonl
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.common import outcome, read, run, tilde  # noqa: E402


DONE = {}


def read_or_done(name):
    return DONE.get(name) or read("quant")[name]


def vector_row():
    return read("hnsw")["mwm2gb"]


def at(row, ef):
    return next(p for p in row["points"] if p["param"] == ef)


def measure(name):
    v = vector_row()
    if name == "halfvec":
        ix = stack.vbench("index", "-kind", "halfvec", "-m", "16", "-efc", "200")
        s = stack.vbench("halfvec", "-values", "40,160")
        ratio = v["index_bytes~"] / ix["index_bytes"]
        gaps = [abs(p["recall_at_10"] - at(v, p["param"])["recall_at_10~"]) for p in s["points"]]
        ok = ratio >= 1.5 and max(gaps) <= 0.01 and "docs_half" in s["plan"]
        return {"probe": "HNSW (embedding_h halfvec_ip_ops), m = 16, ef_construction = 200",
                "tool_cmd": stack.command("index", "-kind", "halfvec") + "; " + stack.command("halfvec", "-values", "40,160"),
                "build_s~": ix["build_s"], "index_bytes~": ix["index_bytes"], "vector_index_bytes~": v["index_bytes~"],
                "smaller_15x": ratio >= 1.5, "ratio~": round(ratio, 2), "plan": s["plan"],
                "points": tilde(s["points"], ("recall_at_10", "p50_us", "p99_us")), "max_gap~": round(max(gaps), 4),
                "same_recall": max(gaps) <= 0.01, "outcome": outcome(ok),
                "detail": "индекс %d МБ против %d МБ у vector; recall@10 %s при ef_search 40 / 160" % (
                    ix["index_bytes"] >> 20, v["index_bytes~"] >> 20,
                    " / ".join("%.3f" % p["recall_at_10"] for p in s["points"]))}
    if name == "hamming":
        h = stack.vbench("hamming", "-values", "10,100,1000")
        sg, ce = h["sign"], h["centered"]
        ok = (sg["skewed_dims"] > 0 and ce["skewed_dims"] == 0 and ce["recall_at_10"]["1000"] >= 0.95
              and sg["recall_at_10"]["1000"] < 0.75)
        DONE[name] = {"probe": "точный перебор по Хэммингу между знаковыми кодами, без индекса, 200 запросов; "
                         "знак координаты и знак после вычитания среднего вектора корпуса",
                "tool_cmd": stack.command("hamming", "-values", "10,100,1000"),
                "sign_skewed_dims": sg["skewed_dims"], "centered_skewed_dims": ce["skewed_dims"],
                "sign_recall~": sg["recall_at_10"], "centered_recall~": ce["recall_at_10"],
                "sign_code_loses": sg["recall_at_10"]["1000"] < 0.75, "centering_fixes": ce["recall_at_10"]["1000"] >= 0.95,
                "outcome": outcome(ok),
                "detail": "координат с одним знаком у 90%%+ документов: %d, после центрирования %d; recall@10 при пересчёте "
                          "10 / 100 / 1000: знак %s, центрированный %s" % (
                              sg["skewed_dims"], ce["skewed_dims"],
                              " / ".join("%.3f" % sg["recall_at_10"][r] for r in ("10", "100", "1000")),
                              " / ".join("%.3f" % ce["recall_at_10"][r] for r in ("10", "100", "1000")))}
        return DONE[name]
    if name in ("binary", "binary-centered"):
        centered = name == "binary-centered"
        ix = stack.vbench("index", "-kind", name, "-m", "16", "-efc", "200")
        args = ("binary", "-values", "10,100,1000") + (("-centered",) if centered else ())
        s = stack.vbench(*args)
        ratio = v["index_bytes~"] / ix["index_bytes"]
        rec = {p["param"]: p["recall_at_10"] for p in s["points"]}
        row = {"probe": "HNSW ((binary_quantize(embedding%s)::bit(384)) bit_hamming_ops), кандидаты по Хэммингу, "
                        "пересчёт по исходным векторам; hnsw.ef_search = числу кандидатов" % (" − средний вектор" if centered else ""),
               "tool_cmd": stack.command("index", "-kind", name) + "; " + stack.command(*args),
               "build_s~": ix["build_s"], "index_bytes~": ix["index_bytes"], "vector_index_bytes~": v["index_bytes~"],
               "smaller_3x": ratio >= 3, "ratio~": round(ratio, 1), "plan": s["plan"], "uses_index": "docs_bin" in s["plan"],
               "points": tilde(s["points"], ("recall_at_10", "p50_us", "p99_us")),
               "detail": "индекс %d МБ против %d МБ у vector; recall@10 %.3f / %.3f / %.3f при 10 / 100 / 1000 кандидатах" % (
                   ix["index_bytes"] >> 20, v["index_bytes~"] >> 20, rec[10], rec[100], rec[1000])}
        if not centered:
            # revision_after_session_a: знаковый код e5-small теряет и с пересчётом
            ham = read_or_done("hamming")
            loses = rec[1000] < 0.75
            not_better = rec[1000] <= ham["sign_recall~"]["1000"] + 0.01
            row.update(sign_code_loses=loses, graph_not_better_than_exact=not_better,
                       outcome=outcome(ratio >= 3 and loses and not_better and row["uses_index"]))
        else:
            plain = DONE["binary"] if "binary" in DONE else read("quant")["binary"]
            plain1000 = next(p["recall_at_10~"] for p in plain["points"] if p["param"] == 1000)
            same_size = abs(ix["index_bytes"] / plain["index_bytes~"] - 1) <= 0.1
            ok = rec[1000] >= 0.9 and rec[1000] - plain1000 >= 0.2 and same_size and row["uses_index"]
            row.update(rescored_1000_ok=rec[1000] >= 0.9, beats_plain=rec[1000] - plain1000 >= 0.2,
                       same_size=same_size, outcome=outcome(ok))
        DONE[name] = row
        return row
    raise KeyError(name)


if __name__ == "__main__":
    run("quant", measure, ["halfvec", "hamming", "binary", "binary-centered"])
