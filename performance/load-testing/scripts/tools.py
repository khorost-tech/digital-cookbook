"""Генераторы стенда: команда запуска и разбор отчёта в одну форму.

Форма отчёта: requests, errors, p50_ms, p90_ms, p99_ms, max_ms (как их
напечатал САМ генератор — это то, что читает человек) и dropped (запросы,
которые генератор собирался, но не смог отправить, — только у k6).

Закрытые режимы — 3 соединения или пользователя без расписания: новый запрос
после ответа. Открытые — 200 запросов/с по расписанию.
"""

import json
import re

URL = "http://svc:8080"
CLOSED_USERS = 3


def _report(out):
    """Строка «REPORT {…}» — её печатают обёртки wrk/wrk2 (lua) и k6 (handleSummary)."""
    lines = [l for l in out.split("\n") if l.startswith("REPORT ")]
    if not lines:
        raise ValueError("нет строки REPORT:\n" + out[-1500:])
    return json.loads(lines[-1][len("REPORT "):])


def _vegeta(out):
    d = json.loads([l for l in out.split("\n") if l.startswith("{")][-1])
    lat = d["latencies"]
    return {"requests": d["requests"], "errors": d["requests"] - int(round(d["success"] * d["requests"])),
            "p50_ms": lat["50th"] / 1e6, "p90_ms": lat["90th"] / 1e6, "p99_ms": lat["99th"] / 1e6,
            "max_ms": lat["max"] / 1e6}


def _oha(out):
    d = json.loads(out[out.index("{"):])
    p, s = d["latencyPercentiles"], d["summary"]
    codes = d.get("statusCodeDistribution", {})
    ok = sum(v for k, v in codes.items() if k == "200")
    total = sum(codes.values()) + sum(d.get("errorDistribution", {}).values())
    return {"requests": total, "errors": total - ok, "p50_ms": p["p50"] * 1e3, "p90_ms": p["p90"] * 1e3,
            "p99_ms": p["p99"] * 1e3, "max_ms": s["slowest"] * 1e3}


def _hey(out):
    # «  99%% in 0.0155 secs» — hey печатает удвоенный процент; «Slowest: 1.0130 secs»
    def q(n):
        return float(re.search(r"^\s+%d%%+ in ([\d.]+) secs" % n, out, re.M).group(1)) * 1e3
    codes = dict((int(c), int(n)) for c, n in re.findall(r"\[(\d+)\]\s+(\d+) responses", out))
    errs = sum(int(n) for n in re.findall(r"^\s+\[(\d+)\]\s+Get .*", out, re.M)) if "Error distribution" in out else 0
    return {"requests": sum(codes.values()) + errs, "errors": errs + sum(n for c, n in codes.items() if c != 200),
            "p50_ms": q(50), "p90_ms": q(90), "p99_ms": q(99),
            "max_ms": float(re.search(r"Slowest:\s+([\d.]+) secs", out).group(1)) * 1e3}


def _locust(out):
    # итоговая таблица перцентилей: строка Aggregated, колонки «50% … 100% # reqs»
    head = [l for l in out.split("\n") if l.startswith("Type") and "50%" in l][-1].split()
    row = [l for l in out.split("\n") if l.strip().startswith("Aggregated")][-1].split()
    cols = dict(zip(head[-13:-2], row[-12:-1]))
    fails = re.findall(r"Aggregated\s+(\d+)\s+(\d+)\(", out)
    return {"requests": int(row[-1]), "errors": int(fails[-1][1]) if fails else 0,
            "p50_ms": float(cols["50%"]), "p90_ms": float(cols["90%"]), "p99_ms": float(cols["99%"]),
            "max_ms": float(cols["100%"])}


def _gatling(out):
    def g(name):
        m = re.search(r"> %s\s+\|\s+([\d,.]+)" % re.escape(name), out)
        return float(m.group(1).replace(",", ""))
    ko = re.search(r"> request count\s+\|\s+[\d,]+\s+\|\s+[\d,]+\s+\|\s+([\d,]+|-)", out).group(1)
    return {"requests": int(g("request count")), "errors": 0 if ko == "-" else int(ko.replace(",", "")),
            "p50_ms": g("response time 50th percentile (ms)"), "p90_ms": g("response time 90th percentile (ms)"),
            "p99_ms": g("response time 99th percentile (ms)"), "max_ms": g("max response time (ms)")}


def build(name, seconds, rate=200, path="/work", users=CLOSED_USERS):
    """(сервис compose, argv, env, разбор, модель) варианта генератора."""
    u, n, c = URL + path, users, "%ds" % seconds
    lua = ["-s", "scenarios/wrk/report.lua"]
    if name == "wrk":
        return "gen", ["wrk", "-t%d" % n, "-c%d" % n, "-d" + c, *lua, u], {}, _report, "closed"
    if name == "wrk2":
        return "gen", ["wrk2", "-t2", "-c16", "-d" + c, "-R%d" % rate, *lua, u], {}, _report, "open"
    if name == "vegeta":
        sh = "echo 'GET %s' | vegeta attack -rate=%d -duration=%s | vegeta report -type=json" % (u, rate, c)
        return "gen", ["sh", "-c", sh], {}, _vegeta, "open"
    if name in ("oha", "oha-corrected"):
        argv = ["oha", "-z", c, "-q", str(rate), "-c", "16", "--no-tui", "--output-format", "json", u]
        if name == "oha-corrected":
            argv.insert(-1, "--latency-correction")
        return "gen", argv, {}, _oha, "open"
    if name == "hey":
        q = ["-q", str(rate // n)] if rate else []  # rate=0 — без ограничения темпа
        return "gen", ["hey", "-z", c, "-c", str(n), *q, u], {}, _hey, "closed"
    if name.startswith("k6-"):
        env = {"DURATION": c, "RATE": rate, "TARGET": u, "VUS": n}
        if name == "k6-closed":
            env["MODE"] = "closed"
        elif name == "k6-open":
            env.update(MODE="open", PRE_VUS=300, MAX_VUS=400)
        elif name == "k6-open-capped":
            env.update(MODE="open", PRE_VUS=20, MAX_VUS=20)
        model = "closed" if name == "k6-closed" else "open"
        return "k6", ["run", "-q", "scenarios/k6/model.js"], env, _report, model
    if name.startswith("gatling-"):
        mode = name.split("-", 1)[1]
        argv = ["mvn", "-q", "-B", "-o", "gatling:test", "-Dmode=" + mode, "-Dseconds=%d" % seconds,
                "-Drate=%d" % rate, "-Dusers=%d" % n, "-Dpath=" + path]
        return "gatling", argv, {}, _gatling, mode
    if name.startswith("locust-"):
        mode = name.split("-")[1]
        argv = ["locust", "-f", "scenarios/locust/model.py", "--headless", "-u", str(n), "-r", str(n), "-t", c,
                "--host", URL, "--only-summary"]
        if name.endswith("-2p"):  # два процесса — по числу ЦП контейнера
            argv[1:1] = ["--processes", "2"]
        return "locust", argv, {"MODE": mode, "USERS": n, "RATE": rate, "TARGET_PATH": path}, _locust, "closed"
    raise KeyError(name)


def command(service, argv, env):
    """Команда для листинга статьи — как её набрать в каталоге стенда."""
    e = "".join("-e %s=%s " % (k, v) for k, v in env.items())
    def q(a):
        return "'%s'" % a if re.search(r"[\s|*']", a) and not a.startswith("'") else a
    if service == "gen" and argv[0] == "sh":
        return "docker compose run --rm %s%s sh -c \"%s\"" % (e, service, argv[2])
    return "docker compose run --rm %s%s %s" % (e, service, " ".join(q(a) for a in argv))
