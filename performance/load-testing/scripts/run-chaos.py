"""Ось chaos: гипотеза стабильного состояния против внесённого сбоя.

Две реплики svc за HAProxy, зависимость dep за Toxiproxy; k6 — 200 запросов/с
на /order 60 с; сбой — с 20-й по 40-ю секунду от запуска k6. Гипотеза
(пороги k6): ошибок < 1%, p99 < 300 мс. Вариант сервиса — MODE: naive (без
таймаута, ошибка зависимости — 502) или resilient (таймаут 100 мс, одна
повторная попытка, запасной ответ).

    python3 scripts/run-chaos.py                → все варианты, fixtures/chaos.jsonl
"""

import datetime
import json
import os
import re
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack, tools  # noqa: E402
from scripts.common import run  # noqa: E402
from scripts.outcome import END_MARKER, MATCH  # noqa: E402

FAULT_AT, FAULT_LEN, RATE = 20, 20, 200
RECOVERY = 5  # окно восстановления после снятия сбоя, с
VARIANTS = ["control-naive", "control-resilient", "latency-naive", "latency-resilient", "down-naive",
            "down-resilient", "down-naive-abort", "kill-replica", "kill-replica-noretry"]
CHAOS = ["dep", "toxiproxy", "svc-a", "svc-b", "lb", "ctl"]

with open(os.path.join(stack.HERE, "expected.json"), encoding="utf-8") as f:
    EXP = json.load(f)["chaos"]


def compose_env(mode, *args, check=True, cfg="haproxy.cfg"):
    p = subprocess.run(["docker", "compose", "--profile", "chaos", *args], cwd=stack.HERE,
                       env=dict(os.environ, MODE=mode, HAPROXY_CFG=cfg), stdout=subprocess.PIPE,
                       stderr=subprocess.STDOUT)
    out = p.stdout.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("compose %s: %s" % (" ".join(args), out[-2000:]))
    return out


def toxi(method, path, body=None):
    argv = ["curl", "-fsS", "-X", method, "http://toxiproxy:8474" + path]
    if body is not None:
        argv += ["-d", json.dumps(body)]
    stack.compose("--profile", "chaos", "exec", "-T", "ctl", *argv)


FAULTS = {
    "latency": (lambda: toxi("POST", "/proxies/dep/toxics",
                             {"name": "lat", "type": "latency", "stream": "downstream", "attributes": {"latency": 500}}),
                lambda: toxi("DELETE", "/proxies/dep/toxics/lat"),
                "Toxiproxy: toxic latency 500 мс на ответы зависимости"),
    "down": (lambda: toxi("POST", "/proxies/dep", {"enabled": False}),
             lambda: toxi("POST", "/proxies/dep", {"enabled": True}),
             "Toxiproxy: прокси зависимости выключен (соединение отвергается)"),
    "kill": (lambda: stack.compose("--profile", "chaos", "kill", "svc-b"),
             lambda: stack.compose("--profile", "chaos", "start", "svc-b"),
             "docker kill реплики svc-b"),
}


def vm_now():
    """Время по часам VM Docker — тем же, по которым k6 ставит метки точек."""
    _, out = stack.compose("--profile", "chaos", "exec", "-T", "ctl", "date", "+%s.%N")
    return float(out.strip().split()[-1])


def points(path):
    """Операции из сырого вывода k6: (начало, длительность мс, ошибка) и возрасты запасных ответов.

    Операция — метрика op_duration: http.get целиком, со всеми повторами клиента; её
    точка ставится в конце операции, значение — длительность по тем же часам, поэтому
    начало = метка − длительность. http_req_duration для этого не годится: без
    установления соединения и, при повторе клиента, без прежней попытки."""
    ops, ages = [], []
    with open(path, encoding="utf-8") as f:
        for line in f:
            if '"type":"Point"' not in line:
                continue
            p = json.loads(line)
            d = p["data"]
            t = datetime.datetime.fromisoformat(d["time"].replace("Z", "+00:00")).timestamp()
            if p["metric"] == "op_duration":
                ops.append((t - d["value"] / 1000, d["value"], d["tags"].get("failed") == "true"))
            elif p["metric"] == "fallback_age_ms":
                ages.append(d["value"])
    return ops, ages


def pct(xs, q):
    s = sorted(xs)
    return s[max(0, min(len(s) - 1, int(q * len(s) + 0.999999) - 1))] if s else None


def window(reqs, lo, hi):
    w = [r for r in reqs if lo <= r[0] < hi]
    errors = sum(1 for r in w if r[2])
    return {"requests": len(w), "errors": errors, "error_rate": round(errors / len(w), 4) if w else None,
            "p99_ms": round(pct([r[1] for r in w], 0.99), 1) if w else None}


def worst_second(reqs):
    """Худшая секунда прогона по доле ошибок (по началу запроса)."""
    by = {}
    for t, _, failed in reqs:
        n, e = by.get(int(t), (0, 0))
        by[int(t)] = (n + 1, e + failed)
    rates = [e / n for n, e in by.values() if n >= 50]
    return round(max(rates), 3) if rates else 0.0


# web svc/b 0/0/-1/-1/4021 503 217 - - sC-- 1/1/0/0/3 0/0 "GET /order HTTP/1.1"
# сервер, таймеры (бывают -1), код, байт клиенту, cookie, флаги завершения, соединения/повторы («+» — redispatch)
HAPROXY_LINE = re.compile(r" svc/(\S+) -?\d+/-?\d+/-?\d+/-?\d+/-?\d+ (\d{3}) (\d+) \S+ \S+ (\S{4}) "
                          r"\d+/\d+/\d+/\d+/(\+?)(\d+) ")


def haproxy_log(client_requests):
    """Журнал HAProxy (option httplog) против счёта k6. Строка журнала — одна попытка
    клиента; попыток больше, чем запросов k6, — клиент повторил запрос сам (Go net/http
    повторяет идемпотентный GET, если переиспользованное соединение закрылось без ответа)."""
    _, out = stack.compose("--profile", "chaos", "logs", "--no-log-prefix", "lb", check=False)
    r = {"attempts": 0, "failed_503": 0, "failed_503_no_bytes": 0, "lb_retried": 0, "redispatched": 0}
    for line in out.split("\n"):
        m = HAPROXY_LINE.search(line)
        if not m:
            continue
        r["attempts"] += 1
        if m.group(2) == "503":
            r["failed_503"] += 1
            r["failed_503_no_bytes"] += m.group(3) == "0"
        r["lb_retried"] += int(m.group(6)) > 0
        r["redispatched"] += m.group(5) == "+"
    r["client_resent"] = r["attempts"] - client_requests
    return r


def measure(name):
    parts = name.split("-")
    kind = parts[0]
    mode = "resilient" if "resilient" in parts else "naive"
    abort = name.endswith("-abort")
    cfg = "haproxy-noretry.cfg" if name.endswith("-noretry") else "haproxy.cfg"
    compose_env(mode, "up", "-d", "--force-recreate", "--wait", *CHAOS, cfg=cfg)
    toxi("POST", "/reset")
    env = {"TARGET": "http://lb:8080/order", "ABORT": "1" if abort else "0"}
    raw = "out/chaos-%s.json" % name
    argv = ["run", "-q", "--out", "json=/opt/stand/" + raw, "scenarios/k6/chaos.js"]
    gen = stack.Background("k6", argv, env)
    fault = FAULTS.get(kind)
    marks = None
    if fault:
        # отметки по часам VM до и после каждого воздействия: команда сама занимает время
        time.sleep(FAULT_AT)
        if gen.running():
            on0 = vm_now()
            fault[0]()
            on1 = vm_now()
            t_host = time.time()
            while gen.running() and time.time() - t_host < FAULT_LEN:
                time.sleep(0.5)
            off0 = vm_now()
            fault[1]()
            off1 = vm_now()
            marks = (on0, on1, off0, off1)
    out = gen.finish()
    reqs, ages = points(os.path.join(stack.HERE, raw))
    rep = tools._report(out)
    ok_err = rep["thresholds_ok"]["http_req_failed"]
    ok_lat = rep["thresholds_ok"]["http_req_duration"]
    ok_op = rep["thresholds_ok"]["op_duration"]
    held = ok_err and ok_lat
    window_n = RATE * FAULT_LEN
    row = {"probe": fault[2] if fault else "без сбоя", "mode": mode, "fault": kind if fault else None,
           "tool_cmd": tools.command("k6", argv, env), "hypothesis_held": held, "errors_ok": ok_err,
           "latency_ok": ok_lat, "requests~": rep["requests"], "errors~": rep["errors"],
           "fallback~": rep["fallback"], "p99_ms~": round(rep["p99_ms"], 1), "max_ms~": round(rep["max_ms"], 1),
           "op_p99_ms~": round(rep["op_p99_ms"], 1), "op_max_ms~": round(rep["op_max_ms"], 1),
           "held_by_operation": ok_err and ok_op,
           "haproxy_cfg": cfg, "run_s~": round(rep["run_ms"] / 1000, 1),
           "fault_window_requests": window_n, "worst_second_error_rate~": worst_second(reqs)}
    if marks:
        on0, on1, off0, off1 = marks
        t0 = min(r[0] for r in reqs)
        # окно сбоя — от «сбой внесён» до «начали снимать»; внесение и снятие — отдельные
        # переходные интервалы (на переключении реплик ошибки как раз там)
        fw = window(reqs, on1, off0)
        rw = window(reqs, off1, off1 + RECOVERY)
        row.update({"fault_marks_s~": {k: round(v - t0, 3) for k, v in
                                       (("on_before", on0), ("on_after", on1), ("off_before", off0), ("off_after", off1))},
                    "insertion_window~": window(reqs, on0, on1), "fault_window~": fw,
                    "removal_window~": window(reqs, off0, off1), "recovery_window~": rw,
                    "held_in_fault_window": fw["error_rate"] is not None and fw["error_rate"] < 0.01
                    and fw["p99_ms"] < 300})
    if rep["fallback"]:
        row.update({"fallback_age_p50_ms~": round(pct(ages, 0.5)), "fallback_age_max_ms~": round(max(ages))})
    if kind == "kill":
        row["haproxy~"] = haproxy_log(rep["requests"])
    e = EXP["expect"][name]
    checks = []
    if "held" in e:
        checks.append(held == e["held"])
    if "held_by_operation" in e:
        checks.append(row["held_by_operation"] == e["held_by_operation"])
    if "errors_range" in e:
        lo, hi = e["errors_range"]
        row["errors_in_range"] = lo <= rep["errors"] <= hi
        checks.append(row["errors_in_range"])
    if "fallback_range" in e:
        lo, hi = e["fallback_range"]
        row["fallback_in_range"] = lo <= rep["fallback"] <= hi
        checks.append(row["fallback_in_range"])
    if "aborted_before_s" in e:
        row["aborted"] = rep["run_ms"] / 1000 < e["aborted_before_s"]
        checks.append(row["aborted"])
    if "held_in_fault_window" in e:
        checks.append(row.get("held_in_fault_window") == e["held_in_fault_window"])
    if "fallback_age_max_range" in e:
        lo, hi = e["fallback_age_max_range"]
        row["fallback_age_in_range"] = lo <= row.get("fallback_age_max_ms~", -1) <= hi
        checks.append(row["fallback_age_in_range"])
    if "redispatched" in e:
        row["no_redispatch"] = row["haproxy~"]["redispatched"] == e["redispatched"]
        checks.append(row["no_redispatch"])
    if "client_resent_min" in e:
        row["client_resent_some"] = row["haproxy~"]["client_resent"] >= e["client_resent_min"]
        checks.append(row["client_resent_some"])
    if "worst_second_min" in e:
        row["worst_second_over"] = row["worst_second_error_rate~"] >= e["worst_second_min"]
        checks.append(row["worst_second_over"])
    row["outcome"] = "match" if all(checks) else "mismatch"
    row["detail"] = "порог по HTTP-метрике %s, по операции %s: ошибок %d из %d, запасных ответов %d, " \
                    "p99 HTTP %.0f мс, операции %.0f мс%s" % (
        "выполнен" if held else "не выполнен", "выполнен" if row["held_by_operation"] else "не выполнен",
        rep["errors"], rep["requests"], rep["fallback"], rep["p99_ms"], rep["op_p99_ms"],
        "; эксперимент оборван порогом на %.1f с" % (rep["run_ms"] / 1000) if abort else "")
    return row


def teardown():
    stack.compose("--profile", "chaos", "rm", "-sf", *CHAOS, check=False)


if __name__ == "__main__":
    try:
        run("chaos", measure, VARIANTS, MATCH, END_MARKER)
    finally:
        teardown()
