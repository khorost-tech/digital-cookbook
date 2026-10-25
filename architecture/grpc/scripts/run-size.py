"""Ось 9: размер сообщения → fixtures/size.jsonl.

Сервер отвечает payload заданной длины; клиент принимает с пределом по
умолчанию или с поднятым до 8 МБ. Варианты: 1mb (контроль), 5mb (больше
предела по умолчанию в 4 МБ), 5mb-raised (тот же ответ с поднятым пределом).
Все пары.

    python3 scripts/run-size.py                  → все варианты, фикстура
    python3 scripts/run-size.py go java 5mb      → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, SIZE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "size.jsonl")
MB = 1024 * 1024
VARIANTS = {"1mb": (1 * MB, 0), "5mb": (5 * MB, 0), "5mb-raised": (5 * MB, 8 * MB)}


def measure(cl, srv, name):
    size, max_recv = VARIANTS[name]
    srvname = "grpc-sz-srv"
    docker.server(srvname, srv)
    try:
        args = ["unary", "-target", srvname + ":9090", "-size", str(size)]
        if max_recv:
            args += ["-max-recv", str(max_recv)]
        r = docker.client(cl, *args)
    finally:
        docker.rm(srvname)
    row = {"kind": "size", "variant": "%s->%s/%s" % (cl, srv, name), "probe": "payload%d" % size,
           "cmd": "python3 scripts/run-size.py %s %s %s" % (cl, srv, name), "client": cl, "server": srv,
           "name": name, "size": size, "max_recv": max_recv, "code": r["code"], "message": r["message"],
           "payload_len": r.get("payload_len", 0)}
    if r["code"] == "OK" and r.get("payload_len") == size:
        row["outcome"], row["detail"] = "delivered", "ответ %d байт дошёл" % size
    elif r["code"] == "ResourceExhausted":
        row["outcome"], row["detail"] = "rejected", "ResourceExhausted: %s" % r["message"][:80]
    else:
        row["outcome"], row["detail"] = "error", "код %s: %s" % (r["code"], r["message"][:80])
    SIZE.parse_line(json.dumps(row, ensure_ascii=False))
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
    for (cl, srv), name in itertools.product(itertools.product(docker.KINDS, repeat=2), VARIANTS):
        rows.append(measure(cl, srv, name))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
