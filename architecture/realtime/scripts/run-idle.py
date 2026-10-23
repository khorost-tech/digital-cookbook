"""Ось 6: цена простаивающего соединения → fixtures/idle.jsonl.

Единственная ось с величиной, а не исходом. Сервер без шины; базовый замер
памяти — дважды, после принудительной сборки мусора (/debug/gc: Go — runtime.GC
и FreeOSMemory, Java — System.gc); затем проба открывает K простаивающих
соединений WS или SSE и держит их; замер — снова после сборки. Память на
соединение — прирост RSS процесса (VmRSS) и прирост кучи, делённые на K.

JVM — с фиксированной кучей (-Xms256m -Xmx256m, Dockerfile): RSS Java после
сборки не возвращает уже занятые страницы кучи, поэтому прирост RSS у неё —
оценка сверху, а куча показывает, что заняли сами соединения.

    python3 scripts/run-idle.py                 → все варианты, фикстура
    python3 scripts/run-idle.py go ws           → один вариант, строка JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "idle.jsonl")
SRV = "rt-id-srv"
SERVERS = ("go", "java")
TRANSPORTS = ("ws", "sse")
K = 5000


def gc():
    return json.loads(docker.sh("docker", "exec", SRV, "wget", "-qO-", "--post-data=",
                                "http://127.0.0.1:8080/debug/gc"))


def measure(server, transport):
    docker.server(SRV, server)
    base1 = gc()
    base2 = gc()
    p = docker.probe_interactive("hold", "-transport", transport, "-url", "http://%s:8080" % SRV,
                                 "-topic", docker.topic("ih"), "-clients", str(K))
    ready = p.stdout.readline().strip()
    loaded = gc()
    p.stdin.write("\n")
    p.stdin.flush()
    out, _ = p.communicate(timeout=120)
    docker.rm(SRV)
    opened = int(ready.split()[1]) if ready.startswith("READY") else 0
    rss = (loaded["rss_kb"] - base2["rss_kb"]) / K
    heap = (loaded["heap_kb"] - base2["heap_kb"]) / K
    row = {"kind": "idle-mem", "variant": "%s/%s" % (server, transport), "probe": "k%d" % K,
           "cmd": "python3 scripts/run-idle.py %s %s" % (server, transport), "server": server,
           "transport": transport, "k": K, "opened": opened, "base1": base1, "base2": base2, "loaded": loaded,
           "rss_kb_per_conn": round(rss, 1), "heap_kb_per_conn": round(heap, 1),
           "base_drift_kb": abs(base2["rss_kb"] - base1["rss_kb"]),
           "jvm_heap": "-Xms256m -Xmx256m" if server == "java" else None}
    return row


SHOW = ("variant", "opened", "rss_kb_per_conn", "heap_kb_per_conn", "base_drift_kb")


def main():
    docker.network()
    args = sys.argv[1:]
    try:
        if args:
            if len(args) != 2 or args[0] not in SERVERS or args[1] not in TRANSPORTS:
                raise SystemExit(__doc__)
            row = measure(*args)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
            return
        docker.build()
        rows = []
        for server in SERVERS:
            for transport in TRANSPORTS:
                row = measure(server, transport)
                rows.append(row)
                print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    finally:
        docker.rm(SRV)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
