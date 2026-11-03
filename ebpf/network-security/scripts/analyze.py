"""Разбор осей стенда ebpf/network-security: контроль каждой оси первым.

    python3 scripts/analyze.py filter   → разбор оси, код 0 — контроли сошлись
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import MATCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
AXES = ("filter", "cilium")


def read(axis):
    return MATCH.read_fixture(os.path.join(HERE, "fixtures", "%s.jsonl" % axis))


def check(axis, rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if axis == "filter":
        if by["none"]["outcome"] != "match":
            raise SystemExit("контроль: без фильтра дошли не все пакеты")
        for r in rows:  # отправитель отправил всё, что собирался; очередь сокета ничего не отбросила
            if any(v != 10000 for v in r["sent"].values()):
                raise SystemExit("%s: отправитель отправил не N" % r["variant"])
            if r["socket_drops"]:
                raise SystemExit("%s: очередь сокета отбросила %d — опыт мерит не фильтр" % (r["variant"], r["socket_drops"]))
    if axis == "cilium":
        k = by["kubeproxy"]
        if (k["svc_chains_lb"], k["sep_chains_lb"]) != (k["services"], k["endpoints"]):
            raise SystemExit("контроль: цепочки kube-proxy не совпали с эталоном")


def main():
    axis = sys.argv[1]
    rows = read(axis)
    check(axis, rows)
    for r in rows:
        print("%-12s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
