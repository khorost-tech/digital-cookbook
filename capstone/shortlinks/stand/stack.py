"""Управление локальным стеком (docker compose) и HTTP к сервису на 127.0.0.1."""

import json
import os
import re
import subprocess
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ["docker", "compose", "-f", str(ROOT / "deploy/compose.yml")]
BASE = "http://127.0.0.1:18080"
CONSUMER = "http://127.0.0.1:19090"


def sh(argv, env=None, check=True, timeout=600):
    p = subprocess.run(argv, capture_output=True, text=True, env=env, timeout=timeout)
    if check and p.returncode:
        raise RuntimeError("command failed: %r\n%s\n%s" % (argv, p.stdout[-2000:], p.stderr[-2000:]))
    return p


def compose(*args, env=None, check=True):
    e = dict(os.environ)
    e.update({k: str(v) for k, v in (env or {}).items()})
    return sh(COMPOSE + list(args), env=e, check=check)


def up(env=None, services=()):
    compose("up", "-d", "--wait", *services, env=env)
    wait_ready()


def down():
    compose("--profile", "obs", "down", "-v", "--remove-orphans", check=False)


def wait_ready(timeout=60):
    t0 = time.time()
    while time.time() - t0 < timeout:
        try:
            if status("/readyz") == 200:
                return
        except Exception:  # noqa: BLE001
            pass
        time.sleep(0.2)
    raise RuntimeError("сервис не готов")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


OPENER = urllib.request.build_opener(NoRedirect)


def request(path, method="GET", body=None, timeout=10):
    """(код, тело, секунды); сетевая ошибка — код 0."""
    data = body.encode() if isinstance(body, str) else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(BASE + path, data=data, method=method)
    t = time.perf_counter()
    try:
        with OPENER.open(req, timeout=timeout) as r:
            return r.status, r.read().decode(), time.perf_counter() - t
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode(), time.perf_counter() - t
    except Exception as e:  # noqa: BLE001 — соединение сброшено/отказано: это и есть исход
        return 0, type(e).__name__, time.perf_counter() - t


def status(path):
    return request(path, timeout=2)[0]


def create(url="https://example.com/landing"):
    code, body, _ = request("/links", "POST", {"url": url})
    if code != 201:
        raise RuntimeError("создание ссылки: %s %s" % (code, body))
    return json.loads(body)["code"]


def metrics(base=BASE):
    with urllib.request.urlopen(base + "/metrics", timeout=5) as r:
        text = r.read().decode()
    out = {}
    for m in re.finditer(r"^([a-z_]+)(?:\{[^}]*\})? (\S+)$", text, re.M):
        out[m.group(1)] = out.get(m.group(1), 0.0) + float(m.group(2))
    return out


def burst(paths, workers):
    """Одновременные запросы: все потоки стартуют по барьеру. Результаты в порядке paths."""
    res = [None] * len(paths)
    barrier = threading.Barrier(workers)
    it = iter(range(len(paths)))
    lock = threading.Lock()

    def worker():
        barrier.wait()
        while True:
            with lock:
                i = next(it, None)
            if i is None:
                return
            res[i] = request(paths[i])
    ts = [threading.Thread(target=worker) for _ in range(workers)]
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    return res


def probe(path, n, c):
    """Залп изнутри сети compose (Go-клиент): настоящая одновременность, без проброса порта."""
    out = sh(["docker", "run", "--rm", "--network", "capstone_default", "shortlinks-probe:dev",
              "-url", "http://service:8080" + path, "-n", str(n), "-c", str(c)]).stdout
    r = json.loads(out.strip().splitlines()[-1])
    return {int(k): v for k, v in r["codes"].items()}, r["median_s"]


def psql(sql):
    return compose("exec", "-T", "postgres", "psql", "-U", "app", "-d", "shortlinks", "-tAc", sql).stdout.strip()


def clicks(code):
    return json.loads(request("/links/%s/stats" % code)[1])["clicks"]


def wait_clicks(code, want, timeout=30):
    t0 = time.time()
    n = -1
    while time.time() - t0 < timeout:
        n = clicks(code)
        if n >= want:
            time.sleep(1.0)       # дать дойти лишним (дублям), если они есть
            return clicks(code)
        time.sleep(0.3)
    return n
