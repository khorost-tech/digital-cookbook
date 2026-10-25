"""Паритет: один набор вызовов (`client check`) во всех четырёх парах клиент-сервер
→ fixtures/protocol.jsonl. Оси сравнивают Go и Java, и сравнивать можно, только
пока контракт и поведение по умолчанию у них одни.

    python3 scripts/run-protocol.py
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, PROTOCOL  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "protocol.jsonl")
CMD = "python3 scripts/run-protocol.py"


def main():
    docker.network()
    if not sys.argv[1:]:
        docker.build()
    rows = []
    try:
        for srv in docker.KINDS:
            name = docker.server("grpc-parity-" + srv, srv)
            for cl in docker.KINDS:
                obs = docker.client(cl, "check", "-target", name + ":9090")
                obs.pop("client")
                rows.append({"kind": "protocol", "variant": "%s->%s" % (cl, srv), "probe": "check", "cmd": CMD,
                             "client": cl, "server": srv, "observations": obs})
            docker.rm(name)
    finally:
        docker.rm(*("grpc-parity-" + k for k in docker.KINDS))
    base = rows[0]["observations"]
    for r in rows:
        same = r["observations"] == base
        r["outcome"] = "same" if same else "differs"
        r["detail"] = "наблюдения совпали с go->go" if same else "расходятся с go->go"
        PROTOCOL.parse_line(json.dumps(r, ensure_ascii=False))
        print(json.dumps({"variant": r["variant"], "outcome": r["outcome"]}, ensure_ascii=False))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
