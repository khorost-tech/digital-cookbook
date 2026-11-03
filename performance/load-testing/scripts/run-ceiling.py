"""Ось ceiling: где генератор перестаёт держать заданный темп.

/fast (ответ без работы), 10 с на темп; у всех генераторов один лимит ЦП
контейнера (2). Открытые — заданный темп 1000 / 4000 / 8000 / 16000 в секунду,
держит — сервис принял ≥ 95% заданного. Закрытые — 64 соединения без
ограничения темпа: сколько выжали. Счёт — по сервису. ЦП генератора —
docker stats во время прогона (100% — одно ядро, лимит — 200%).

    python3 scripts/run-ceiling.py               → все варианты, fixtures/ceiling.jsonl
"""

import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack, tools  # noqa: E402
from scripts.common import run  # noqa: E402
from scripts.outcome import END_MARKER, MATCH  # noqa: E402

SECONDS = 10
CPU_SECONDS = 30  # docker stats отвечает раз в ≈2,5 с — за 10 с вышло бы 3–4 замера
OPEN = ["wrk2", "vegeta", "oha-corrected", "k6-open", "gatling-open"]
RATES = [1000, 4000, 8000, 16000]
CLOSED = ["wrk", "hey", "locust-closed", "locust-closed-2p"]
# ЦП генератора — отдельными прогонами («+cpu»): опрос docker stats сам нагружает VM
# и сбивает генератор у его предела, поэтому темп меряется без опроса
CPU = ["gatling-open@8000", "gatling-open@16000", "k6-open@16000", "wrk2@16000", "locust-closed@max",
       "locust-closed-2p@max"]
VARIANTS = ["%s@%d" % (t, r) for t in OPEN for r in RATES] + ["%s@max" % t for t in CLOSED] + \
    ["%s+cpu" % v for v in CPU]

with open(os.path.join(stack.HERE, "expected.json"), encoding="utf-8") as f:
    EXP = json.load(f)["ceiling"]


def measure(name):
    sample = name.endswith("+cpu")
    tool, rate = name[:-len("+cpu")].split("@") if sample else name.split("@")
    closed = rate == "max"
    rate = 0 if closed else int(rate)
    seconds = CPU_SECONDS if sample else SECONDS
    service, argv, env, parse, _ = tools.build(tool, seconds, rate, path="/fast", users=64 if closed else 3)
    t_reset = time.time()
    stack.admin("/admin/reset")
    gen = stack.Background(service, argv, env)
    cpu = []  # (середина вызова docker stats по часам хоста, ЦП %)
    while gen.running():
        if not sample:
            time.sleep(0.5)
            continue
        a = time.time()
        c = gen.cpu(service)  # сам вызов docker stats длится ≈1 с — это и есть шаг опроса
        if c is not None:
            cpu.append(((a + time.time()) / 2, c))
    gen.finish()
    st = stack.stats()
    # ЦП — только за интервал нагрузки: от первого прихода на сервис до последней
    # секунды приходов, без крайних секунд; старт JVM и Maven у Gatling сюда не попадает
    secs = [int(k) for k in st["per_sec"]]
    lo = t_reset + st["first_arrival_ms"] / 1000 + 1 if secs else 0
    hi = t_reset + max(secs) if secs else 0
    busy = sorted(c for t, c in cpu if lo <= t <= hi)
    achieved = st["requests"] / seconds
    row = {"probe": "%s, /fast, %d с, %s" % (tool, seconds, "64 соединения" if closed else "%d/с" % rate),
           "tool": tool, "target_rate": rate or None, "tool_cmd": tools.command(service, argv, env),
           "achieved_rate~": round(achieved)}
    if sample:
        row["cpu_pct_median_load~"] = round(busy[len(busy) // 2]) if busy else None
        row["cpu_samples_load~"] = len(busy)
        row["outcome"] = "match" if busy else "error"
        row["detail"] = "ЦП генератора — медиана за интервал нагрузки %s%% по %d замерам (сервис принял %.0f/с под опросом)" % (
            row["cpu_pct_median_load~"], len(busy), achieved)
        return row
    if closed:
        row["outcome"] = "match"
        row["detail"] = "закрытый, 64 соединения: %.0f/с" % achieved
        return row
    holds = achieved >= 0.95 * rate
    row["holds"] = holds
    exp = EXP["expect_holds"].get(name)
    row["outcome"] = "match" if exp is None or exp == holds else "mismatch"
    row["detail"] = "%s: сервис принял %.0f/с из %d/с" % ("держит" if holds else "не держит", achieved, rate)
    return row


if __name__ == "__main__":
    run("ceiling", measure, VARIANTS, MATCH, END_MARKER)
