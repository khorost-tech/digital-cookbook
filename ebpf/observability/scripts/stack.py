"""Единственное место, где стенд трогает Docker: compose, генератор, выгрузка коллектора.

Сервис svc-plain видит Beyla (eBPF), svc-sdk инструментирован OTEL SDK; оба
отдают трейсы в коллектор, который пишет их в out/traces.jsonl.
"""

import datetime as dt
import json
import os
import subprocess
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(HERE, "out")


def sh(*argv, check=True, timeout=900):
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
    for _ in range(3):
        rc, out = compose("up", "-d", "--build", "--wait", check=False, timeout=1800)
        if rc == 0:
            break
    else:
        raise SystemExit("стек не поднялся:\n" + out[-2000:])
    tools("go", "build", "-o", "bin/gen", "./cmd/gen")


def tools(*args, check=True):
    return compose("exec", "-T", "tools", *args, check=check)


def result(out):
    line = [l for l in out.split("\n") if l.startswith("RESULT ")][-1]
    return json.loads(line[len("RESULT "):])


def reset_telemetry(timeout=60):
    """Пустая выгрузка: коллектор и Beyla перезапускаются вместе — файл нельзя
    удалить на ходу, а Beyla после перезапуска коллектора спанов не отдаёт.
    Готовность — строка «instrumenting process» в журнале Beyla после старта."""
    compose("stop", "beyla", "otelcol")
    for f in ("traces.jsonl", "metrics.jsonl"):
        p = os.path.join(OUT, f)
        if os.path.exists(p):
            os.remove(p)
    compose("start", "otelcol")
    since = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    compose("start", "beyla")
    start = time.time()
    while time.time() - start < timeout:
        _, logs = compose("logs", "--since", since, "beyla", check=False)
        if "instrumenting process" in logs and "svc-plain" in logs:
            return
        time.sleep(1)
    raise SystemExit("Beyla не прицепилась к svc-plain за %d с" % timeout)


def warmup(who, service, timeout=180):
    """Ждать, пока инструментирование реально видит запросы: строка в журнале
    Beyla появляется раньше, чем зонды начинают работать. Возвращает, сколько
    запросов прогрева ушло до первого спана /warmup."""
    start, n = time.time(), 0
    while time.time() - start < timeout:
        tools("/opt/stand/bin/gen", who, "warmup")
        n += 1
        time.sleep(2)
        if any(svc == service and sp["name"].endswith("/warmup") for svc, sp, _ in spans()):
            return n
    raise SystemExit("%s: спан /warmup не появился за %d с" % (service, timeout))


def gen(target):
    _, out = tools("/opt/stand/bin/gen", target)
    return result(out)


def spans():
    """[(service, span, атрибуты)] из выгрузки коллектора."""
    path = os.path.join(OUT, "traces.jsonl")
    if not os.path.exists(path):
        return []
    res = []
    with open(path) as f:
        for line in f:
            for rs in json.loads(line)["resourceSpans"]:
                svc = [a["value"].get("stringValue") for a in rs["resource"]["attributes"]
                       if a["key"] == "service.name"][0]
                for ss in rs["scopeSpans"]:
                    for sp in ss["spans"]:
                        attrs = {a["key"]: list(a["value"].values())[0] for a in sp.get("attributes", [])}
                        res.append((svc, sp, attrs))
    return res


def settle(services, timeout=180):
    """Ждать, пока число спанов каждого сервиса перестанет расти (три чтения
    подряд, 5 с между ними): Beyla шлёт пачками и позже SDK."""
    hist, start = [], time.time()
    while time.time() - start < timeout:
        cur = tuple(sum(1 for svc, _, _ in spans() if svc == s) for s in services)
        hist.append(cur)
        if len(hist) >= 3 and hist[-1] == hist[-2] == hist[-3] and all(cur):
            return spans()
        time.sleep(5)
    raise SystemExit("спаны не перестали приходить за %d с" % timeout)

