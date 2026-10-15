"""Блокирующая проверка: пять целей отдают ПОБАЙТОВО одинаковые тела.

Без неё весь стенд бессмыслен. Три фреймворка по умолчанию настроены
по-разному — у Django сессии и CSRF, у FastAPI сериализация ответа через
pydantic, у Flask сортировка ключей, — и замер «одинаковой» ручки без
выравнивания измеряет, кто меньше делает, а результат выдаёт за скорость.

Поэтому: одинаковый вход, побайтовое сравнение тел, и отказ при любом
расхождении. Плюс печать версий, снятых из самих пакетов.

Запуск: python bench/check_equivalence.py
"""

import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
from importlib.metadata import version

APP_DIR = "/app"
PY = "/opt/py/web/bin/python"

# Каждое приложение — под своим естественным сервером. Разницу серверов из
# результата не вычесть, поэтому в наборе есть контрольные плечи raw-asgi и
# raw-wsgi: они дают пол, над которым читается надбавка фреймворка.
TARGETS = [
    ("fastapi",  8001, [PY, "-m", "uvicorn", "app_fastapi:app",
                        "--port", "8001", "--log-level", "warning"]),
    ("django",   8002, [PY, "-m", "uvicorn", "app_django:application",
                        "--port", "8002", "--log-level", "warning"]),
    ("raw-asgi", 8004, [PY, "-m", "uvicorn", "app_raw:asgi_app",
                        "--port", "8004", "--log-level", "warning"]),
    ("flask",    8003, [PY, "-m", "gunicorn", "-b", "127.0.0.1:8003",
                        "--log-level", "warning", "app_flask:app"]),
    ("raw-wsgi", 8005, [PY, "-m", "gunicorn", "-b", "127.0.0.1:8005",
                        "--log-level", "warning", "app_raw:wsgi_app"]),
]

SAMPLE = {"id": 42, "name": "тестовое имя", "tags": ["a", "b", "c"]}
BAD_SAMPLE = {"id": 0, "name": "", "tags": ["ok"], "extra": 1}


def request(port, path, payload=None):
    url = f"http://127.0.0.1:{port}{path}"
    data = None
    headers = {}
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["content-type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers,
                                 method="POST" if data else "GET")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()


def wait_ready(port, proc, timeout=25.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            return False
        try:
            status, _ = request(port, "/ping")
            if status == 200:
                return True
        except OSError:
            pass
        time.sleep(0.2)
    return False


def main():
    print("версии, снятые из самих пакетов:")
    for pkg in ("fastapi", "pydantic", "django", "flask", "uvicorn",
                "gunicorn"):
        print(f"  {pkg:<10} {version(pkg)}")
    print(f"  {'python':<10} {sys.version.split()[0]}")
    print()

    procs = []
    failures = []
    try:
        for name, port, cmd in TARGETS:
            proc = subprocess.Popen(cmd, cwd=APP_DIR,
                                    stdout=subprocess.DEVNULL,
                                    stderr=subprocess.DEVNULL)
            procs.append((name, port, proc))
        for name, port, proc in procs:
            if not wait_ready(port, proc):
                failures.append(f"{name}: не поднялся на порту {port}")
        if failures:
            for f in failures:
                print("SMOKE FAIL: " + f, file=sys.stderr)
            return 1

        # Три эндпоинта плюс отвергаемый ввод: если валидация у кого-то
        # слабее, статусы разойдутся, и это тоже расхождение работы.
        cases = [
            ("GET  /ping", "/ping", None),
            ("POST /echo", "/echo", SAMPLE),
            ("POST /wait", "/wait", SAMPLE),
            # /cpu отдаёт в теле контрольную сумму счёта: совпадение тел здесь
            # доказывает, что все пятеро ПОСЧИТАЛИ одно и то же, а не только
            # вернули одинаковую форму ответа.
            ("POST /cpu", "/cpu", SAMPLE),
            ("POST /slow", "/slow", SAMPLE),
            ("POST /echo (плохой ввод)", "/echo", BAD_SAMPLE),
        ]

        for label, path, payload in cases:
            results = {}
            for name, port, _ in procs:
                results[name] = request(port, path, payload)

            statuses = {n: r[0] for n, r in results.items()}
            bodies = {n: r[1] for n, r in results.items()}

            uniq_status = set(statuses.values())
            uniq_body = set(bodies.values())

            ok = len(uniq_status) == 1 and len(uniq_body) == 1
            print(f"  {'ОК  ' if ok else 'СБОЙ'} {label}: "
                  f"статус {sorted(uniq_status)}, "
                  f"различных тел {len(uniq_body)}")
            if not ok:
                for name in sorted(results):
                    print(f"       {name:<9} {statuses[name]} {bodies[name]!r}")
                failures.append(label)

        # Отдельно: для отвергаемого ввода статус обязан быть 4xx, иначе
        # «одинаковые тела» ничего не значат — все могли принять мусор.
        status_bad, _ = request(procs[0][1], "/echo", BAD_SAMPLE)
        if not 400 <= status_bad < 500:
            failures.append(f"плохой ввод принят со статусом {status_bad}")
            print(f"  СБОЙ плохой ввод не отвергнут: статус {status_bad}")
    finally:
        for _, _, proc in procs:
            proc.terminate()
        for _, _, proc in procs:
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()

    print()
    if failures:
        print(f"SMOKE FAIL: расхождений {len(failures)}", file=sys.stderr)
        return 1
    print("SMOKE OK: пять целей отдают побайтово одинаковые тела")
    return 0


if __name__ == "__main__":
    sys.exit(main())
