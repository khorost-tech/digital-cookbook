"""Разбор осей стенда ebpf/fundamentals: контроль каждой оси первым.

    python3 scripts/analyze.py hooks   → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("hooks", "maps", "verifier", "core", "bpftrace")


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "hooks":
        t = by["tracepoint"]
        if t["counted"] != t["n"]:
            raise SystemExit("контроль: tracepoint не совпал с нагрузкой")
        for r in rows:  # нагрузка, которая не упала, сделала ровно N
            if r["load_exit"] == "ok" and r["load_done"] != r["n"]:
                raise SystemExit("%s: нагрузка сделала не N" % r["variant"])
    if axis == "maps":
        if by["percpu"]["counted"] != by["percpu"]["n"]:
            raise SystemExit("контроль: per-CPU не совпал с нагрузкой")
        for r in rows:
            if "accounted" in r and not r["accounted"]:
                raise SystemExit("%s: получено + отказов не равно нагрузке" % r["variant"])
    if axis == "verifier" and by["ok"]["stage"] != "accepted":
        raise SystemExit("контроль: корректная программа не принята")
    if axis == "core" and not by["tgid"]["value_matches"]:
        raise SystemExit("контроль: tgid через CO-RE не совпал с PID нагрузки")
    if axis == "bpftrace" and by["open"]["counted"] != by["open"]["n"]:
        raise SystemExit("контроль: bpftrace open не совпал с нагрузкой")


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-16s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
