"""Ось 7: балансировка долгого HTTP/2-соединения → fixtures/balance.jsonl.

Три бэкенда одного сервера (grpc-bl-1..3, общий DNS-псевдоним grpc-bl). Клиент
делает 30 вызовов по ОДНОМУ каналу. Счёт — по ответам (какой бэкенд ответил) и
по бэкендам (сколько соединений каждый принял): «распределились» без лишних
соединений и есть балансировка вызовов, а не соединений.

Пути:
  direct-pick-first   — контроль: dns:///grpc-bl:9090, политика pick_first;
  direct-round-robin  — тот же адрес, клиентская политика round_robin;
  angie-l4, envoy-l4  — прокси пересылает TCP-соединение (stream / tcp_proxy);
  angie-l7, envoy-l7  — прокси разбирает HTTP/2 (grpc_pass / http_connection_manager).

    python3 scripts/run-balance.py                       → все варианты, фикстура
    python3 scripts/run-balance.py go java envoy-l4      → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import BALANCE, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "balance.jsonl")
BACKENDS = ("grpc-bl-1", "grpc-bl-2", "grpc-bl-3")
N = 30
PATHS = ("direct-pick-first", "direct-round-robin", "angie-l4", "envoy-l4", "angie-l7", "envoy-l7")


def measure(cl, srv, path):
    for b in BACKENDS:
        docker.server(b, srv, aliases=("grpc-bl",))
    prx = None
    try:
        if path.startswith("direct"):
            args = ["balance", "-target", "dns:///grpc-bl:9090", "-lb", path.split("-", 1)[1].replace("-", "_")]
        else:
            kind, layer = path.split("-")
            prx = docker.proxy(kind, layer)
            args = ["balance", "-target", prx + ":9000"]
        r = docker.client(cl, *args, "-n", str(N))
        conns = {b: docker.stats(b)["conns"] for b in BACKENDS}
    finally:
        docker.rm(*BACKENDS, *([prx] if prx else []))
    row = {"kind": "balance", "variant": "%s->%s/%s" % (cl, srv, path), "probe": "n%d-one-channel" % N,
           "cmd": "python3 scripts/run-balance.py %s %s %s" % (cl, srv, path), "client": cl, "server": srv,
           "path": path, "calls": r["calls"], "errors": r["errors"], "per_instance": r["per_instance"],
           "instances": r["instances"], "backend_conns": conns}
    if r["errors"]:
        row["outcome"], row["detail"] = "error", "ошибок вызова: %d" % r["errors"]
    elif r["instances"] == len(BACKENDS):
        row["outcome"] = "spread"
        row["detail"] = "вызовы по бэкендам: %s" % ", ".join(str(v) for _, v in sorted(r["per_instance"].items()))
    elif r["instances"] == 1:
        row["outcome"], row["detail"] = "pinned", "все %d вызовов на одном бэкенде" % N
    else:
        row["outcome"], row["detail"] = "error", "вызовы на %d бэкендах из 3" % r["instances"]
    BALANCE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    if sys.argv[1:]:
        row = measure(*sys.argv[1:4])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for (cl, srv), path in itertools.product(itertools.product(docker.KINDS, repeat=2), PATHS):
        rows.append(measure(cl, srv, path))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
