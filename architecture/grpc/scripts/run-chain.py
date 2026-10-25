"""Ось 4: порядок интерсепторов → fixtures/chain.jsonl.

Цепочка из трёх интерсепторов — авторизация, метрики, журнал — в двух порядках:
auth-first (авторизация первой) и metrics-first (авторизация последней).
Клиент делает 10 вызовов без токена и 10 с токеном. Счёт на сервере: сколько
вызовов увидели метрики (и с какими кодами), журнал и обработчик. Все пары.

Опыт panic-metrics: обработчик паникует; перехватчик паники стоит снаружи
метрик (recover,metrics,log) или внутри, ближе к обработчику (metrics,log,
recover). Один вызов с паникой и один без; счёт метрик по кодам.

    python3 scripts/run-chain.py                          → все варианты, фикстура
    python3 scripts/run-chain.py go java auth-first       → один вариант, строка JSON
    python3 scripts/run-chain.py go java recover-outer    → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import CHAIN, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "chain.jsonl")
N = 10
ORDERS = ("auth-first", "metrics-first")
PLACES = {"recover-outer": "recover,metrics,log", "recover-inner": "metrics,log,recover"}


def measure(cl, srv, order):
    name = "grpc-ch-srv"
    docker.server(name, srv, {"GRPC_CHAIN_ORDER": order})
    try:
        r = docker.client(cl, "calls", "-target", name + ":9090", "-n", str(N))
        st = docker.stats(name)
    finally:
        docker.rm(name)
    row = {"kind": "chain", "variant": "%s->%s/%s" % (cl, srv, order), "probe": "n%d+n%d" % (N, N),
           "cmd": "python3 scripts/run-chain.py %s %s %s" % (cl, srv, order), "client": cl, "server": srv,
           "order": order, "client_codes": r["codes"], "metrics_calls": st["metrics_calls"],
           "metrics_by_code": st["metrics_by_code"], "log_calls": st["log_calls"], "handler_calls": st["handler_calls"]}
    if r["codes"] != {"Unauthenticated": N, "OK": N}:
        row["outcome"], row["detail"] = "error", "клиент получил не то: %s" % r["codes"]
    else:
        row["outcome"] = "observed"
        row["detail"] = "метрики видели %d (%s), журнал %d, обработчик %d" % (
            st["metrics_calls"], ", ".join("%s %d" % kv for kv in sorted(st["metrics_by_code"].items())),
            st["log_calls"], st["handler_calls"])
    CHAIN.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def panic_metrics(cl, srv, place):
    name = "grpc-ch-srv"
    docker.server(name, srv, {"GRPC_CHAIN": PLACES[place]})
    try:
        r = docker.client(cl, "unary", "-target", name + ":9090", "-mode", "panic")
        ok = docker.client(cl, "unary", "-target", name + ":9090", "-mode", "ok")
        st = docker.stats(name)
    finally:
        docker.rm(name)
    row = {"kind": "panic-metrics", "variant": "%s->%s/%s" % (cl, srv, place), "probe": "panic+ok",
           "cmd": "python3 scripts/run-chain.py %s %s %s" % (cl, srv, place), "client": cl, "server": srv,
           "place": place, "chain": PLACES[place], "client_code": r["code"], "control_code": ok["code"],
           "metrics_calls": st["metrics_calls"], "metrics_by_code": st["metrics_by_code"]}
    if ok["code"] != "OK" or st["metrics_by_code"].get("OK") != 1:
        row["outcome"], row["detail"] = "error", "контроль: вызов без паники не виден метрикам"
    else:
        row["outcome"] = "observed"
        row["detail"] = "клиент получил %s; метрики: %s" % (
            r["code"], ", ".join("%s %d" % kv for kv in sorted(st["metrics_by_code"].items())))
    CHAIN.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    if sys.argv[1:]:
        row = (panic_metrics if sys.argv[3] in PLACES else measure)(*sys.argv[1:4])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for (cl, srv), order in itertools.product(itertools.product(docker.KINDS, repeat=2), ORDERS):
        rows.append(measure(cl, srv, order))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    for (cl, srv), place in itertools.product(itertools.product(docker.KINDS, repeat=2), PLACES):
        rows.append(panic_metrics(cl, srv, place))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
