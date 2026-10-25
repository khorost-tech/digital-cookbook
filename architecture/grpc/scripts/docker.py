"""Единственное место, где собирается `docker run` стенда grpc.

Свои образы — grpc-go (сервер, клиент-проба, шлюз) и grpc-java (сервер,
клиент-проба) — собираются из исходников стенда; сторонние — по дайджесту из
images.json. Раннеры осей зовут функции отсюда, а не строят команды сами.
"""

import json
import os
import subprocess
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NET = "grpc-net"
IMAGE = {"go": "grpc-go", "java": "grpc-java"}
KINDS = ("go", "java")

with open(os.path.join(HERE, "images.json"), encoding="utf-8") as _f:
    IMAGES = json.load(_f)


def pinned(name):
    img = IMAGES[name]
    return "%s@%s" % (img["ref"], img["digest"])


def sh(*argv, check=True, timeout=600):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace").strip()
    if check and p.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(argv), out[-2000:]))
    return out


def build():
    sh("docker", "build", "-q", "-t", IMAGE["go"], os.path.join(HERE, "go"), timeout=1800)
    sh("docker", "build", "-q", "-t", IMAGE["java"], "-f", os.path.join(HERE, "java", "Dockerfile"), HERE,
       timeout=1800)


def network():
    if NET not in sh("docker", "network", "ls", "--format", "{{.Name}}"):
        sh("docker", "network", "create", NET)


def rm(*names):
    if names:
        sh("docker", "rm", "-f", *names, check=False)


def stats(name):
    return json.loads(sh("docker", "exec", name, "wget", "-qO-", "http://127.0.0.1:8081/stats"))


def reset(name):
    sh("docker", "exec", name, "wget", "-qO-", "--post-data=", "http://127.0.0.1:8081/reset", check=False)


def server(name, kind, env=None, aliases=()):
    """Сервер (go|java) без автоперезапуска: упавший сервер остаётся упавшим."""
    rm(name)
    argv = ["docker", "run", "-d", "--name", name, "--network", NET, "-e", "GRPC_INSTANCE=" + name]
    for a in aliases:
        argv += ["--network-alias", a]
    for k, v in (env or {}).items():
        argv += ["-e", "%s=%s" % (k, v)]
    sh(*argv, IMAGE[kind], "server")
    for _ in range(120):
        out = sh("docker", "exec", name, "wget", "-qO-", "http://127.0.0.1:8081/stats", check=False)
        if out.startswith("{"):
            return name
        time.sleep(0.5)
    raise SystemExit("%s не поднялся: %s" % (name, sh("docker", "logs", "--tail", "30", name, check=False)))


def alive(name):
    return sh("docker", "inspect", "-f", "{{.State.Running}}", name, check=False) == "true"


def client(kind, *args, timeout=600):
    """Подкоманда клиента-пробы (go|java) в сети стенда; итог — последняя строка JSON."""
    out = sh("docker", "run", "--rm", "--network", NET, IMAGE[kind], "client", *args, timeout=timeout)
    lines = [l for l in out.splitlines() if l.startswith("{")]
    if not lines:
        raise SystemExit("клиент %s %s: нет итога\n%s" % (kind, " ".join(args), out[-2000:]))
    return json.loads(lines[-1])


def gateway(name, target, binary="gateway"):
    """gRPC-Gateway (образ grpc-go) перед сервером target; binary — gateway или
    gateway-details (с зарегистрированными типами деталей ошибок)."""
    rm(name)
    sh("docker", "run", "-d", "--name", name, "--network", NET, "-e", "GRPC_TARGET=" + target, IMAGE["go"], binary)
    time.sleep(1)
    return name


def proxy(kind, layer, name="grpc-proxy"):
    """Прокси (angie|envoy) уровня layer (l4|l7) на :9000; конфиг — angie/<layer>.conf
    (вместо angie.conf целиком: stream живёт вне http) или envoy/<layer>.yaml."""
    rm(name)
    if kind == "angie":
        conf = os.path.join(HERE, "angie", layer + ".conf")
        sh("docker", "run", "-d", "--name", name, "--network", NET, "-v", "%s:/etc/angie/angie.conf:ro" % conf,
           pinned("angie"))
        time.sleep(1)
        out = sh("docker", "exec", name, "angie", "-t", check=False)
        if "successful" not in out:
            raise SystemExit("angie %s: %s" % (layer, out))
    else:
        conf = os.path.join(HERE, "envoy", layer + ".yaml")
        sh("docker", "run", "-d", "--name", name, "--network", NET, "-v", "%s:/etc/envoy/envoy.yaml:ro" % conf,
           pinned("envoy"))
        for _ in range(40):
            if "all clusters initialized" in sh("docker", "logs", name, check=False) or \
                    "starting main dispatch loop" in sh("docker", "logs", name, check=False):
                break
            time.sleep(0.5)
        else:
            raise SystemExit("envoy %s не поднялся: %s" % (layer, sh("docker", "logs", "--tail", "20", name, check=False)))
    return name
