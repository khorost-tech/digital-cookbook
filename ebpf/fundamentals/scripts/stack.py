"""Единственное место, где стенд трогает Docker: compose, сборка, запуск загрузчиков.

Контейнер tools привилегированный, с pid: host — загрузчик (cmd/*) запускает
нагрузку (cmd/load), загружает eBPF-программы с её PID и печатает RESULT.
"""

import json
import os
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CMDS = ("load", "hooks", "maps", "verifier", "core", "bt")


def sh(*argv, check=True, timeout=900):
    p = subprocess.run(argv, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("%s: rc=%d\n%s" % (" ".join(argv), p.returncode, out[-3000:]))
    return p.returncode, out


def compose(*args, **kw):
    return sh("docker", "compose", *args, **kw)


def up(fresh=False):
    if fresh:
        compose("down", "-v", check=False)
    for _ in range(3):
        rc, out = compose("up", "-d", "--build", "--wait", check=False, timeout=1800)
        if rc == 0:
            break
    else:
        raise SystemExit("стек не поднялся:\n" + out[-2000:])
    build()


def tools(*args, check=True):
    return compose("exec", "-T", "tools", *args, check=check)


def build():
    """vmlinux.h из BTF ядра стенда, bpf2go и сборка всех команд — в контейнере."""
    script = ("set -e; [ -s bpf/vmlinux.h ] || bpftool btf dump file /sys/kernel/btf/vmlinux format c > bpf/vmlinux.h; "
              "go generate ./cmd/...; ") + "; ".join("go build -o bin/%s ./cmd/%s" % (c, c) for c in CMDS)
    tools("sh", "-c", script)


def result(out):
    line = [l for l in out.split("\n") if l.startswith("RESULT ")][-1]
    return json.loads(line[len("RESULT "):])


def run(cmd, *args):
    """Запуск загрузчика: RESULT даже при ненулевом коде (нагрузка могла упасть)."""
    _, out = tools("/opt/stand/bin/%s" % cmd, *[str(a) for a in args], check=False)
    return result(out)
