"""Единственное место, где собирается `docker run` стенда.

Раннеры осей зовут функции отсюда, а не строят команды сами: иначе один вариант
шёл бы с одним набором флагов, другой — с другим, и различие исходов было бы
различием обвязки. Сторонние образы — по дайджесту из images.json; свои
(rt-server-go, rt-server-java, rt-probe) собираются из исходников стенда.
"""

import json
import os
import subprocess
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NET = "rt-net"
LOCAL = {"go": "rt-server-go", "java": "rt-server-java", "probe": "rt-probe"}
SOURCES = {"go": "server-go", "java": "server-java", "probe": "probe"}

with open(os.path.join(HERE, "images.json"), encoding="utf-8") as _f:
    IMAGES = json.load(_f)


def pinned(name):
    """Сторонний образ по дайджесту: тег для человека, дайджест для воспроизводимости."""
    img = IMAGES[name]
    return "%s@%s" % (img["ref"], img["digest"])


def sh(*argv, check=True, timeout=300):
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace").strip()
    if check and p.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(argv), out[-2000:]))
    return out


def build(which=("go", "java", "probe")):
    for k in which:
        sh("docker", "build", "-q", "-t", LOCAL[k], os.path.join(HERE, SOURCES[k]), timeout=1800)


def network():
    if NET not in sh("docker", "network", "ls", "--format", "{{.Name}}"):
        sh("docker", "network", "create", NET)


def rm(*names):
    if names:
        sh("docker", "rm", "-f", *names, check=False)


def wait_healthy(name, port=8080, tries=120):
    for _ in range(tries):
        out = sh("docker", "exec", name, "wget", "-qO-", "http://127.0.0.1:%d/healthz" % port, check=False)
        if out.startswith("{"):
            return json.loads(out)
        time.sleep(0.5)
    raise SystemExit("%s не поднялся: %s" % (name, sh("docker", "logs", "--tail", "30", name, check=False)))


def server(name, kind, env=None, wait=True):
    """Инстанс сервера (go|java) с именем name — оно же RT_INSTANCE и адрес в сети."""
    rm(name)
    argv = ["docker", "run", "-d", "--name", name, "--network", NET, "-e", "RT_INSTANCE=" + name]
    for k, v in (env or {}).items():
        argv += ["-e", "%s=%s" % (k, v)]
    sh(*argv, LOCAL[kind])
    if wait:
        wait_healthy(name)
    return name


def redis(name="redis"):
    rm(name)
    sh("docker", "run", "-d", "--name", name, "--network", NET, pinned("redis"))
    for _ in range(60):
        if sh("docker", "exec", name, "redis-cli", "ping", check=False) == "PONG":
            return name
        time.sleep(0.5)
    raise SystemExit("redis не поднялся")


def nats(name="nats"):
    rm(name)
    sh("docker", "run", "-d", "--name", name, "--network", NET, pinned("nats"), "-js")
    time.sleep(1)
    return name


def angie(conf, name="angie"):
    """Angie с конфигом варианта из angie/<conf>.conf вместо default.conf образа."""
    rm(name)
    path = os.path.join(HERE, "angie", conf + ".conf")
    sh("docker", "run", "-d", "--name", name, "--network", NET,
       "-v", "%s:/etc/angie/http.d/default.conf:ro" % path, pinned("angie"))
    time.sleep(1)
    out = sh("docker", "exec", name, "angie", "-t", check=False)
    if "successful" not in out:
        raise SystemExit("angie %s: %s" % (conf, out))
    return name


def proxy(name, target):
    """Считающий/рвущий TCP-прокси пробы: :9000 → target, управление :9100."""
    rm(name)
    sh("docker", "run", "-d", "--name", name, "--network", NET, LOCAL["probe"], "proxy",
       "-listen", ":9000", "-target", target, "-control-listen", ":9100")
    time.sleep(0.5)
    return name


def probe(*args, timeout=600):
    """Подкоманда пробы в сети стенда; итог — последняя строка JSON."""
    out = sh("docker", "run", "--rm", "--network", NET, LOCAL["probe"], *args, timeout=timeout)
    lines = [l for l in out.splitlines() if l.startswith("{")]
    if not lines:
        raise SystemExit("проба %s: нет итога\n%s" % (" ".join(args), out[-2000:]))
    return json.loads(lines[-1])


def probe_interactive(*args):
    """Подкоманда пробы, которая печатает READY и ждёт строку на stdin."""
    return subprocess.Popen(["docker", "run", "--rm", "-i", "--network", NET, LOCAL["probe"], *args],
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)


def topic(prefix):
    """Своя тема на каждый замер: сервер помнит недавние события темы, и повтор
    темы дал бы старые события в новом замере (найдено на дымовой проверке)."""
    # Номер — ровно девять цифр: имя темы входит в каждое событие, и тема другой
    # длины изменила бы байты на событие (ось 1).
    return "%s-%09d" % (prefix, time.time_ns() % 10**9)
