"""Паритет протокола Go и Java → fixtures/protocol.jsonl.

Один набор запросов (`probe check`) к свежему инстансу каждого сервера;
наблюдения — без имён инстансов. Оси 2–5 гоняются против обоих серверов, и
сравнивать их можно, только пока протокол один.

    python3 scripts/run-protocol.py
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "protocol.jsonl")
CMD = "python3 scripts/run-protocol.py"


def observe(kind):
    name = docker.server("rt-parity-" + kind, kind)
    try:
        return docker.probe("check", "-url", "http://%s:8080" % name)
    finally:
        docker.rm(name)


def main():
    docker.network()
    if not sys.argv[1:]:
        docker.build()
    rows = [{"kind": "protocol", "variant": kind, "cmd": CMD, "observations": observe(kind)} for kind in ("go", "java")]
    same = rows[0]["observations"] == rows[1]["observations"]
    print(json.dumps({"same": same, "keys": len(rows[0]["observations"])}, ensure_ascii=False))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
