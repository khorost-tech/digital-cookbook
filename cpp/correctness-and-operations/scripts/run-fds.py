"""Ось 5: дескрипторы на пути ошибки → fixtures/fds.jsonl.

Сервис оси 3, маршрут /file?fail=0|1, владение дескриптором — env
STAND_FILE_OWNERSHIP (raw | raii). Сервис — PID 1 в контейнере; дескрипторы
считаются `ls /proc/1/fd` внутри контейнера до и после M запросов. Запросы
шлёт клиент в сети Docker и считает коды ответов.

    python3 scripts/run-fds.py                     → фикстура
    python3 scripts/run-fds.py <raw|raii> <0|1>    → одна строка
"""

import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, read_measurements  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "fds.jsonl")
SVC = "cpp-correctness-and-operations-svc:latest"
CLIENT = "python@sha256:3dd7cc108ec1493442514f5c2a871af6af0ec31d768ff6e378a93340c3b3db5f"
NET = "cpp-correctness-net"
M = 100

LOOP = ("import socket,sys,json,collections\n"
        "c=collections.Counter()\n"
        "for _ in range(int(sys.argv[3])):\n"
        " s=socket.create_connection((sys.argv[1],8080),timeout=5)\n"
        " s.sendall(('GET /file?fail=%s HTTP/1.1\\r\\n\\r\\n'%sys.argv[2]).encode());d=b''\n"
        " while True:\n  x=s.recv(4096)\n  if not x: break\n  d+=x\n"
        " s.close();c[d.split(b' ')[1].decode()]+=1\n"
        "print(json.dumps(c))")


def sh(*argv):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if p.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(argv[:4]), p.stdout.decode()))
    return p.stdout.decode().strip()


def fds(box):
    return int(sh("docker", "exec", box, "bash", "-c", "ls /proc/1/fd | wc -l"))


def measure(ownership, fail):
    box = "fds-%s-%d" % (ownership, fail)
    subprocess.run(["docker", "rm", "-f", box], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    sh("docker", "run", "-d", "--name", box, "--network", NET, "-e", "STAND_PORT=8080",
       "-e", "STAND_TIMEOUT_MS=1000", "-e", "STAND_FILE_OWNERSHIP=" + ownership, SVC)
    time.sleep(1)
    before = fds(box)
    responses = json.loads(sh("docker", "run", "--rm", "--network", NET, CLIENT, "python", "-c", LOOP,
                              box, str(fail), str(M)))
    time.sleep(0.5)
    after = fds(box)
    sh("docker", "rm", "-f", box)
    return {"kind": "fds", "variant": "%s/fail=%d" % (ownership, fail),
            "cmd": "python3 scripts/run-fds.py %s %d" % (ownership, fail),
            "ownership": ownership, "fail": fail, "m": M, "source": "/proc/1/fd",
            "fd_before": before, "fd_after": after, "delta": after - before, "responses": responses}


def main():
    docker.build_base()
    sh("docker", "build", "-q", "-t", SVC, "-f", os.path.join(docker.HERE, "images", "Dockerfile.svc"),
       docker.HERE)
    if NET not in sh("docker", "network", "ls", "--format", "{{.Name}}").split():
        sh("docker", "network", "create", NET)
    if len(sys.argv) == 3:
        print(json.dumps(measure(sys.argv[1], int(sys.argv[2])), ensure_ascii=False))
        return
    rows = [measure(o, f) for o in ("raw", "raii") for f in (0, 1)]
    for r in rows:
        print(json.dumps({k: r[k] for k in ("variant", "fd_before", "fd_after", "delta", "responses")}))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")
    read_measurements(FIXTURE)


if __name__ == "__main__":
    main()
