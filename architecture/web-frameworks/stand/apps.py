"""Двенадцать приложений с одним и тем же API: сборка образов, запуск, ожидание готовности, память.

Пары «минимальный — полный» в шести экосистемах. Каждое приложение — минимальный идиоматичный код без библиотек
сверх самого фреймворка; поведение по умолчанию — то, что и сравнивается.
"""

import json
import subprocess
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PAIRS = {"go": ("go-nethttp", "go-gin"), "rust": ("rust-axum", "rust-actix"), "python": ("py-flask", "py-fastapi"),
         "node": ("node-express", "node-nestjs"), "java": ("java-javalin", "java-spring"), "csharp": ("cs-minimal", "cs-mvc")}
APPS = [a for pair in PAIRS.values() for a in pair]
PORT = {a: 18100 + i for i, a in enumerate(APPS)}


def sh(argv, check=True, timeout=1800):
    p = subprocess.run(argv, capture_output=True, text=True, timeout=timeout)
    if check and p.returncode:
        raise RuntimeError("command failed: %r\n%s" % (argv, (p.stdout + p.stderr)[-3000:]))
    return p


def tag(app):
    return "web-fw-%s:dev" % app


def build(app):
    """Сборка с повторами: сбой сети при скачивании зависимостей — не результат опыта."""
    for n in range(5):
        p = sh(["docker", "build", "-q", "-t", tag(app), str(ROOT / "apps" / app)], check=False)
        if p.returncode == 0:
            return
        time.sleep(10)
    raise RuntimeError("сборка %s не прошла: %s" % (app, p.stderr[-2000:]))


def name(app):
    return "web-fw-" + app


def start(app):
    """Запуск и время до первого ответа 200 на GET /items/1 — секунды от docker run."""
    sh(["docker", "rm", "-f", name(app)], check=False)
    t0 = time.time()
    sh(["docker", "run", "-d", "--name", name(app), "-p", "127.0.0.1:%d:8080" % PORT[app], tag(app)])
    while time.time() - t0 < 120:
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d/items/1" % PORT[app], timeout=1) as r:
                if r.status == 200:
                    return time.time() - t0
        except Exception:  # noqa: BLE001 — ещё не готов
            pass
        time.sleep(0.05)
    raise RuntimeError("%s не ответил за 120 с:\n%s" % (app, sh(["docker", "logs", name(app)], check=False).stdout[-2000:]))


def stop(app):
    sh(["docker", "rm", "-f", name(app)], check=False)


def rss_mib(app):
    """Память контейнера по docker stats — МиБ."""
    out = sh(["docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", name(app)]).stdout.strip().split("/")[0].strip()
    num, unit = float(out[:-3]), out[-3:]
    return num * {"KiB": 1 / 1024, "MiB": 1, "GiB": 1024}[unit]


def base(app):
    return "http://127.0.0.1:%d" % PORT[app]
