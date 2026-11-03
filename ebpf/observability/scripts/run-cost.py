"""Ось 3: цена eBPF-инструментирования — счётчики ядра программ Beyla.

kernel.bpf_stats_enabled=1: у каждой программы run_cnt (срабатываний) и
run_time_ns (время внутри программы). Программы Beyla — те, что появились
после её запуска (bpftool в VM не показывает владельцев). Два окна:
  idle — без нагрузки (контроль: что программы делают на фоне);
  load — генератор отправляет план запросов на svc-plain.
На запрос — разница счётчиков окна load за вычетом фона той же длины,
делённая на число запросов. Это время ядра в eBPF-коде, а не задержка запроса;
точные величины от прогона к прогону разные («~»).

    python3 scripts/run-cost.py              → фикстура
"""

import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import COST, END_MARKER  # noqa: E402

STATE = {}
IDLE = 20


def progs():
    _, out = stack.tools("bpftool", "prog", "show", "-j")
    return {p["id"]: p for p in json.loads(out)}


def totals(snap, ids):
    return (sum(snap[i].get("run_cnt", 0) for i in ids if i in snap),
            sum(snap[i].get("run_time_ns", 0) for i in ids if i in snap))


def prepare():
    stack.compose("stop", "beyla")
    before = set(progs())
    stack.reset_telemetry()
    stack.warmup("svc-plain", "svc-beyla")
    ids = sorted(set(progs()) - before)
    stack.tools("sysctl", "-w", "kernel.bpf_stats_enabled=1")
    s0 = progs()
    time.sleep(IDLE)
    s1 = progs()
    t = time.time()
    truth = stack.gen("svc-plain")
    load_s = time.time() - t
    s2 = progs()
    stack.tools("sysctl", "-w", "kernel.bpf_stats_enabled=0")
    requests = sum(sum(v.values()) for v in truth["sent"].values())
    (c0, n0), (c1, n1), (c2, n2) = totals(s0, ids), totals(s1, ids), totals(s2, ids)
    idle_cnt, idle_ns = c1 - c0, n1 - n0
    load_cnt, load_ns = c2 - c1, n2 - n1
    bg = load_s / IDLE  # фон за время нагрузки
    fired = sum(1 for i in ids if s2.get(i, {}).get("run_cnt", 0) > s1.get(i, {}).get("run_cnt", 0))
    STATE.update({"programs": len(ids), "types": sorted({s2[i]["type"] for i in ids if i in s2}),
                  "requests": requests, "fired_under_load": fired, "load_s": load_s,
                  "idle": {"run_cnt": idle_cnt, "run_time_ns": idle_ns},
                  # фон за время нагрузки против срабатываний под нагрузкой
                  "background_share": idle_cnt * bg / load_cnt if load_cnt else 0,
                  "per_request": {"run_cnt": (load_cnt - idle_cnt * bg) / requests,
                                  "run_time_ns": (load_ns - idle_ns * bg) / requests}})


def measure(name):
    if not STATE:
        prepare()
    if name == "idle":
        # фон от прогона к прогону разный (0 или десятки срабатываний — фоновая работа самого
        # сервиса); устойчиво — что он меньше 1% срабатываний под нагрузкой
        share = STATE["background_share"]
        return {"probe": "%d s without load" % IDLE, "programs~": STATE["programs"],
                "idle_run_cnt~": STATE["idle"]["run_cnt"], "background_below_1pct": share < 0.01,
                "outcome": "match" if share < 0.01 else "mismatch",
                "detail": "программ Beyla %d; за %d с без нагрузки срабатываний %d — меньше 1%% срабатываний под нагрузкой" % (
                    STATE["programs"], IDLE, STATE["idle"]["run_cnt"])}
    pr = STATE["per_request"]
    return {"probe": "generator plan, %d requests" % STATE["requests"], "requests": STATE["requests"],
            "programs~": STATE["programs"], "fired_under_load~": STATE["fired_under_load"],
            "run_cnt_per_request~": round(pr["run_cnt"], 1), "run_time_us_per_request~": round(pr["run_time_ns"] / 1000, 1),
            "outcome": "match",
            "detail": "обращений (HTTP и gRPC) %d: программ сработало %d из %d, срабатываний на обращение %.0f, время в программах %.1f мкс на обращение" % (
                STATE["requests"], STATE["fired_under_load"], STATE["programs"], pr["run_cnt"], pr["run_time_ns"] / 1000)}


if __name__ == "__main__":
    common.run("cost", measure, ("idle", "load"), COST, END_MARKER)
