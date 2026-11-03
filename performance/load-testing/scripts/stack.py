"""Единственное место, где стенд трогает Docker: compose, сервис, запуск генератора."""

import json
import os
import subprocess
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(HERE, "out")


def sh(*argv, check=True, timeout=1800):
    p = subprocess.run(argv, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("%s: rc=%d\n%s" % (" ".join(argv), p.returncode, out[-3000:]))
    return p.returncode, out


def compose(*args, **kw):
    return sh("docker", "compose", *args, **kw)


def up(fresh=False):
    os.makedirs(OUT, exist_ok=True)
    if fresh:
        compose("down", "-v", check=False)
    # сборка тянет метаданные базовых образов из Docker Hub — его 504 и обрывы TLS
    # переживаются паузой, а не немедленным повтором
    for i in range(5):
        rc, out = compose("--profile", "gen", "build", "svc", "gen", check=False, timeout=3600)
        if rc == 0:
            break
        time.sleep(30 * (i + 1))
    else:
        raise SystemExit("образы не собрались:\n" + out[-2000:])
    compose("up", "-d", "--wait", "svc")
    # зависимости Gatling — заранее и с повторами: сеть Docker Desktop рвёт TLS
    # (bad_record_mac), а во время замера сети быть не должно (mvn -o)
    for _ in range(4):
        rc, out = compose("run", "--rm", "-T", "gatling", "mvn", "-q", "-B", "dependency:go-offline", "test-compile",
                          check=False)
        if rc == 0:
            return
    raise SystemExit("зависимости Gatling не скачались:\n" + out[-2000:])


def admin(path, svc="svc"):
    """POST в админку сервиса изнутри его контейнера (порт наружу не публикуется)."""
    compose("exec", "-T", svc, "curl", "-fsS", "-XPOST", "http://localhost:8080" + path)


def stats(svc="svc"):
    _, out = compose("exec", "-T", svc, "curl", "-fsS", "http://localhost:8080/admin/stats")
    return json.loads(out)


def run(service, *argv, env=None, timeout=900):
    """Генератор — одноразовый контейнер; возвращает (rc, вывод)."""
    e = []
    for k, v in (env or {}).items():
        e += ["-e", "%s=%s" % (k, v)]
    rc, out = compose("run", "--rm", "-T", *e, service, *argv, check=False, timeout=timeout)
    # строки compose о контейнерах — не вывод генератора
    out = "\n".join(l for l in out.split("\n") if not l.startswith((" Container ", " Volume ", " Network ")))
    return rc, out


class Background:
    """Генератор в фоне, пока раннер вносит сбой или опрашивает сервис. Вывод —
    в файл, не в pipe: после сбоя k6 пишет предупреждение на каждый неудачный
    запрос, pipe переполняется, и генератор встаёт на записи, а раннер ждёт его."""

    def __init__(self, service, argv, env=None):
        import tempfile
        e = []
        for k, v in (env or {}).items():
            e += ["-e", "%s=%s" % (k, v)]
        self.log = tempfile.TemporaryFile()
        self.p = subprocess.Popen(["docker", "compose", "run", "--rm", "-T", *e, service, *argv], cwd=HERE,
                                  stdout=self.log, stderr=subprocess.STDOUT)
        self.t0 = time.time()

    def running(self):
        return self.p.poll() is None

    def cpu(self, service):
        """ЦП контейнера генератора сейчас, % одного ядра (docker stats; 200% — оба ЦП лимита)."""
        _, out = sh("docker", "stats", "--no-stream", "--format", "{{.Name}} {{.CPUPerc}}", check=False)
        vals = [float(l.split()[1].rstrip("%")) for l in out.split("\n")
                if l.startswith("load-testing-%s-run-" % service) and len(l.split()) == 2]
        return max(vals) if vals else None

    def finish(self, timeout=1800):
        self.p.wait(timeout=timeout)
        self.log.seek(0)
        return self.log.read().decode("utf-8", "replace")
