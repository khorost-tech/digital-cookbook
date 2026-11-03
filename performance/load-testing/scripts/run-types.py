"""Ось types: виды нагрузочных тестов на k6 (открытая модель) против расчёта.

    load       400/с, 60 с — SLO (p99 < 50 мс, ошибок < 1%) выполняется; контроль
    stress     200 → 1000/с за 60 с — пропускная способность сервиса против C/S
    spike      400/с, всплеск 1200/с на 5 с — разгрузка очереди против расчёта
    spike-few-vus  то же с 200 заранее созданными VU — k6 недодаёт сам всплеск
    soak-short 100/с на сервис с утечкой и лимитом памяти, 30 с — проходит
    soak       то же 300 с — сервис убит OOM, тест видит ошибки

    python3 scripts/run-types.py                 → все варианты, fixtures/types.jsonl
"""

import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack, tools, truth  # noqa: E402
from scripts.common import run  # noqa: E402
from scripts.outcome import END_MARKER, MATCH  # noqa: E402

VARIANTS = ["load", "stress", "spike", "spike-few-vus", "soak-short", "soak"]
S_MS = truth.S_MS

with open(os.path.join(stack.HERE, "expected.json"), encoding="utf-8") as f:
    EXP_TYPES = json.load(f)["types"]
TOL = EXP_TYPES["tolerance"]


def k6(type_, **env):
    e = {"TYPE": type_, **env}
    argv = ["run", "-q", "scenarios/k6/types.js"]
    return argv, e


def report(out):
    return tools._report(out)


def r1(x):
    return round(x, 1)


def measure_load():
    argv, env = k6("load")
    stack.admin("/admin/reset")
    _, out = stack.run("k6", *argv, env=env)
    rep, st = report(out), stack.stats()
    ok = rep["thresholds_ok"]["http_req_duration"] and rep["thresholds_ok"]["http_req_failed"]
    return {"probe": "k6 constant-arrival-rate 400/с, 60 с", "tool_cmd": tools.command("k6", argv, env),
            "slo_met": ok, "requests~": rep["requests"], "server_requests~": st["requests"],
            "p99_ms~": r1(rep["p99_ms"]), "server_p50_ms~": r1(st["p50_ms"]), "server_p99_ms~": r1(st["p99_ms"]),
            "outcome": "match" if ok else "mismatch",
            "detail": "SLO %s: p99 %.1f мс, ошибок %d из %d" % ("выполнен" if ok else "НЕ выполнен", rep["p99_ms"],
                                                              rep["errors"], rep["requests"])}


def measure_stress():
    argv, env = k6("stress")
    stack.admin("/admin/reset")
    _, out = stack.run("k6", *argv, env=env)
    rep, st = report(out), stack.stats()
    done = sorted(st["done_per_sec"].values(), reverse=True)
    top = done[:5]
    capacity = sum(top) / len(top)
    cap_truth = truth.knee()
    # колено: первая секунда прихода, после которой худшее время в окне 100 мс
    # больше 3·S и до конца не возвращается; темп в этот момент — по плану k6
    first = st["first_arrival_ms"] / 1000
    by = sorted((int(k) / 10 - first, v) for k, v in st["max_ms_by_100ms"].items())
    knee_t = None
    for t, v in by:
        if v > 3 * S_MS:
            knee_t = t if knee_t is None else knee_t
        else:
            knee_t = None
    knee_rate = 200 + 800 * knee_t / 60 if knee_t is not None else None
    within = abs(capacity - cap_truth) <= TOL["capacity_rel"] * cap_truth
    # независимый прогноз: C / средняя занятость обработчика (без ожидания в очереди)
    cap_busy = truth.C / (st["busy_mean_ms"] / 1000)
    return {"probe": "k6 ramping-arrival-rate 200 → 1000/с, 60 с", "tool_cmd": tools.command("k6", argv, env),
            "capacity_truth": cap_truth, "capacity_measured~": r1(capacity), "capacity_within": within,
            "busy_mean_ms~": round(st["busy_mean_ms"], 2), "capacity_from_busy~": r1(cap_busy),
            "capacity_matches_busy": abs(capacity - cap_busy) <= TOL["capacity_rel"] * cap_busy,
            "knee_rate~": r1(knee_rate) if knee_rate else None, "slo_met": rep["thresholds_ok"]["http_req_duration"],
            "p99_ms~": r1(rep["p99_ms"]), "max_ms~": r1(rep["max_ms"]), "dropped~": rep["dropped_iterations"],
            "outcome": "match" if within else "mismatch",
            "detail": "обслужено до %.0f/с (5 лучших секунд) против C/S = %.0f/с и C/занятость = %.0f/с; "
                      "задержка уходит вверх с темпа %s/с" % (
                          capacity, cap_truth, cap_busy, "%.0f" % knee_rate if knee_rate else "—")}


def spike_rate(st):
    """Принято сервисом в секунду во время всплеска (секунды 21–26 от первого прихода)."""
    first = int(st["first_arrival_ms"] // 1000)
    per = {int(k) - first: v for k, v in st["per_sec"].items()}
    return sum(per.get(t, 0) for t in range(21, 27)) / 6


def measure_spike_few():
    argv, env = k6("spike", PRE_VUS=200)
    stack.admin("/admin/reset")
    _, out = stack.run("k6", *argv, env=env)
    rep, st = report(out), stack.stats()
    rate = spike_rate(st)
    ok = rep["dropped_iterations"] > EXP_TYPES["spike_few_vus"]["dropped_min"] and rate < 1000
    return {"probe": "то же, 200 заранее созданных VU", "tool_cmd": tools.command("k6", argv, env),
            "dropped~": rep["dropped_iterations"], "spike_rate_server~": round(rate), "spike_rate_planned": 1200,
            "outcome": "match" if ok else "mismatch",
            "detail": "во время всплеска сервис принимал %.0f/с из 1200/с; k6 не отправил %d" % (
                rate, rep["dropped_iterations"])}


def measure_spike():
    argv, env = k6("spike")
    stack.admin("/admin/reset")
    _, out = stack.run("k6", *argv, env=env)
    rep, st = report(out), stack.stats()
    # окна сервиса — от его сброса; сдвигаем к первому приходу, как в плане k6
    first = int(st["first_arrival_ms"] // 100)
    buckets = {int(k) - first: v for k, v in st["max_ms_by_100ms"].items()}
    measured = truth.recovered_at(buckets, truth.SPIKE_END, 1.5 * st["busy_mean_ms"] + 5)
    # истина — по номиналу S и по фактической занятости обработчика: сервис меряет её
    # отдельно от ожидания семафора и паузы (busy), а не p50 запроса целиком
    arr = truth.stages(*truth.SPIKE)
    nominal = truth.spike_recovery()
    s_eff = st["busy_mean_ms"] / 1000
    effective = truth.recovery(arr, truth.simulate(arr, s=s_eff), truth.SPIKE_END, s=s_eff)
    within = measured is not None and abs(measured - effective) <= TOL["recovery_abs_s"]
    return {"probe": "k6 ramping-arrival-rate 400/с, всплеск 1200/с на 5 с", "tool_cmd": tools.command("k6", argv, env),
            "dropped~": rep["dropped_iterations"], "server_requests~": st["requests"],
            "spike_rate_server~": round(spike_rate(st)),
            "recovery_truth_nominal_s": nominal, "s_effective_ms~": round(st["busy_mean_ms"], 2),
            "server_p50_ms~": r1(st["p50_ms"]),
            "recovery_truth_effective_s~": effective, "recovery_measured_s~": measured, "recovery_within": within,
            "slo_met": rep["thresholds_ok"]["http_req_duration"], "p99_ms~": r1(rep["p99_ms"]),
            "max_ms~": r1(rep["max_ms"]), "outcome": "match" if within else "mismatch",
            "detail": "очередь разгружена через %s с после всплеска; расчёт: %.1f с по номиналу S, %.1f с по факту; "
                      "k6 не отправил %d" % (measured, nominal, effective, rep["dropped_iterations"])}


def oom_killed():
    _, cid = stack.compose("--profile", "soak", "ps", "-aq", "svc-soak")
    _, out = stack.sh("docker", "inspect", "-f", "{{.State.OOMKilled}} {{.State.Running}}", cid.strip())
    killed, running = out.split()
    return killed == "true", running == "true"


def measure_soak(seconds):
    stack.compose("--profile", "soak", "up", "-d", "--force-recreate", "--wait", "svc-soak")
    argv, env = k6("soak", DURATION="%ds" % seconds, BASE="http://svc-soak:8080")
    base = stack.stats("svc-soak")["heap_inuse_mb"]
    gen = stack.Background("k6", argv, env)
    t0 = gen.t0
    oom_at, heap = None, base
    while gen.running():
        time.sleep(2)
        if oom_at is None:
            killed, running = oom_killed()
            if killed:
                oom_at = time.time() - t0
            elif running:
                try:
                    heap = stack.stats("svc-soak")["heap_inuse_mb"]
                except SystemExit:
                    pass
    out = gen.finish()
    if oom_at is None and oom_killed()[0]:
        oom_at = time.time() - t0
    rep = report(out)
    slo = rep["thresholds_ok"]["http_req_failed"] and rep["thresholds_ok"]["http_req_duration"]
    return {"probe": "k6 100/с на /work?leak=1 (4 КБ на запрос), лимит памяти 96 МБ, %d с" % seconds,
            "tool_cmd": tools.command("k6", argv, env), "seconds": seconds, "oom_killed": oom_at is not None,
            "oom_after_s~": r1(oom_at) if oom_at else None, "heap_growth_mb~": r1(heap - base),
            "slo_met": slo, "errors~": rep["errors"], "requests~": rep["requests"]}


def measure(name):
    if name == "load":
        return measure_load()
    if name == "stress":
        return measure_stress()
    if name == "spike":
        return measure_spike()
    if name == "spike-few-vus":
        return measure_spike_few()
    if name == "soak-short":
        r = measure_soak(30)
        ok = not r["oom_killed"] and r["slo_met"]
        r.update(outcome="match" if ok else "mismatch",
                 detail="30 с: OOM нет, куча выросла на %.1f МБ, SLO %s" % (
                     r["heap_growth_mb~"], "выполнен" if r["slo_met"] else "НЕ выполнен"))
        return r
    if name == "soak":
        r = measure_soak(300)
        lo, hi = TOL["oom_window_s"]
        ok = r["oom_killed"] and lo <= r["oom_after_s~"] <= hi and not r["slo_met"]
        r.update(outcome="match" if ok else "mismatch",
                 detail="300 с: %s, ошибок %d из %d" % (
                     "сервис убит OOM через %.0f с" % r["oom_after_s~"] if r["oom_killed"] else "OOM не было",
                     r["errors~"], r["requests~"]))
        return r
    raise KeyError(name)


if __name__ == "__main__":
    run("types", measure, VARIANTS, MATCH, END_MARKER)
