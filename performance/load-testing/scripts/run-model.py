"""Ось model: одна задача, разные генераторы и модели нагрузки.

Задача — /work, 200 запросов/с (открытые) или 3 соединения (закрытые), 60 с,
сервис замирает на 1 с каждые 10 с. Истина открытой модели — scripts/truth.py
со сдвигом первого прихода относительно расписания пауз (его знает сервис).
Контроль — те же генераторы 20 с без пауз.

    python3 scripts/run-model.py                  → все варианты, fixtures/model.jsonl
    python3 scripts/run-model.py hey vegeta       → только эти, строки JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack, tools, truth  # noqa: E402
from scripts.common import run  # noqa: E402
from scripts.outcome import END_MARKER, MODEL  # noqa: E402

TOOLS = ["wrk", "wrk2", "vegeta", "oha", "oha-corrected", "hey", "k6-closed", "k6-open", "k6-open-capped",
         "gatling-closed", "gatling-open", "locust-closed", "locust-throughput"]
VARIANTS = ["control-" + t for t in TOOLS] + TOOLS
RATE, SECONDS, CONTROL_SECONDS, P, T = 200, 60, 20, 1.0, 10.0

with open(os.path.join(stack.HERE, "expected.json"), encoding="utf-8") as f:
    TOL = json.load(f)["model"]["tolerance"]


def r1(x):
    return round(x, 1)


def measure(name):
    """Контролю — одна повторная попытка: открытый генератор честно засчитывает
    мгновенную паузу хоста (задержку от запланированного момента отправки), и
    такой сбой на 20 с контроля — не свойство генератора. Неудачная попытка
    остаётся в строке (first_attempt~)."""
    row = measure_once(name)
    if name.startswith("control-") and row["outcome"] == "mismatch":
        retry = measure_once(name)
        retry["first_attempt~"] = row["detail"]
        return retry
    return row


def measure_once(name):
    control = name.startswith("control-")
    tool = name[len("control-"):] if control else name
    seconds = CONTROL_SECONDS if control else SECONDS
    service, argv, env, parse, model = tools.build(tool, seconds, RATE)
    stack.admin("/admin/reset")
    if not control:
        stack.admin("/admin/stall?p=%ds&t=%ds" % (P, T))
    rc, out = stack.run(service, *argv, env=env)
    st = stack.stats()
    try:
        rep = parse(out)
    except (ValueError, AttributeError, IndexError, KeyError) as e:
        return {"probe": tool, "outcome": "error", "detail": "отчёт %s не разобран: %s; rc=%d" % (tool, e, rc)}
    row = {"probe": "%s, %d с%s" % (tool, seconds, "" if control else ", паузы 1 с из 10 с"),
           "tool": tool, "model": model, "stalls": not control, "tool_cmd": tools.command(service, argv, env),
           "tool_requests~": rep["requests"], "server_requests~": st["requests"],
           "count_diff_within": abs(rep["requests"] - st["requests"]) <= TOL["count_abs"],
           "errors~": rep["errors"], "dropped~": rep.get("dropped_iterations", 0),
           "p50_ms~": r1(rep["p50_ms"]), "p90_ms~": r1(rep["p90_ms"]), "p99_ms~": r1(rep["p99_ms"]),
           "max_ms~": r1(rep["max_ms"]), "server_p99_ms~": r1(st["p99_ms"]),
           "achieved_rate~": r1(st["requests"] / seconds)}
    hidden = rep["p99_ms"] < TOL["hidden_p99_ms"]
    row["p99_hidden"] = hidden
    row["p90_hidden"] = rep["p90_ms"] < TOL["hidden_p99_ms"]
    row["dropped_any"] = rep.get("dropped_iterations", 0) > 0
    if control:
        ok = hidden and row["count_diff_within"]
        row["outcome"] = "match" if ok else "mismatch"
        row["detail"] = "без пауз: p99 %.1f мс, генератор %d / сервис %d" % (
            rep["p99_ms"], rep["requests"], st["requests"])
        return row
    offset = (st["first_arrival_ms"] - st["stall_offset_ms"]) / 1000
    tr = truth.open_model(RATE, SECONDS, P, T, offset)
    # истина зависит от сдвига первого прихода относительно расписания пауз — он свой в каждом прогоне
    row.update({"truth_p90_ms~": tr["p90_ms"], "truth_p99_ms~": tr["p99_ms"], "truth_max_ms~": tr["max_ms"]})
    row["p99_within"] = abs(rep["p99_ms"] - tr["p99_ms"]) <= TOL["p99_rel"] * tr["p99_ms"]
    row["max_within"] = abs(rep["max_ms"] - tr["max_ms"]) <= TOL["max_rel"] * tr["max_ms"]
    if row["p99_within"] and row["max_within"]:
        row["outcome"] = "matches"
    elif hidden:
        row["outcome"] = "hides"
    else:
        row["outcome"] = "differs"
    row["detail"] = "p90/p99/max %.0f/%.0f/%.0f мс против истины %.0f/%.0f/%.0f; отправлено %d, сервис принял %d" % (
        rep["p90_ms"], rep["p99_ms"], rep["max_ms"], tr["p90_ms"], tr["p99_ms"], tr["max_ms"],
        rep["requests"], st["requests"])
    if rep.get("dropped_iterations"):
        row["detail"] += "; не отправлено (dropped_iterations) %d" % rep["dropped_iterations"]
    return row


if __name__ == "__main__":
    run("model", measure, VARIANTS, MODEL, END_MARKER)
