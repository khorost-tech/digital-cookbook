"""Ось 1: одна нагрузка — разные точки подключения (cmd/hooks, bpf/hooks.c).

  tracepoint / kprobe / fentry — N открытий файла (openat → do_sys_openat2);
  uprobe / uretprobe           — N вызовов листовой Go-функции main.targetFunc;
  uprobe-grow / uretprobe-grow — N вызовов main.targetGrow, внутри растёт стек.
Эталон — счёт нагрузки. Контроль — tracepoint = N.

    python3 scripts/run-hooks.py                  → все варианты, фикстура
    python3 scripts/run-hooks.py uretprobe-grow   → один вариант
"""

import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, HOOKS  # noqa: E402

N = 10000
VARIANTS = {"tracepoint": ("tracepoint", "open"), "kprobe": ("kprobe", "open"), "fentry": ("fentry", "open"),
            "uprobe": ("uprobe", "call"), "uretprobe": ("uretprobe", "call"),
            "uprobe-grow": ("uprobe", "callgrow"), "uretprobe-grow": ("uretprobe", "callgrow")}


def measure(name):
    hook, mode = VARIANTS[name]
    r = stack.run("hooks", hook, mode, N)
    if "error" in r:
        return {"probe": "%s %s %d" % (hook, mode, N), "hook": hook, "mode": mode, "n": N,
                "outcome": "error", "detail": "программа не загрузилась: %s" % r["error"]}
    done = (r.get("load") or {}).get("done")
    crash = re.sub(r"\bg \d+: ", "", r.get("load_crash", "")).strip()  # номер горутины от прогона к прогону разный
    row = {"probe": "%s %s %d" % (hook, mode, N), "hook": hook, "mode": mode, "n": N, "counted": r["counted"],
           "load_done": done, "load_exit": r["load_exit"], "load_crash": crash or None}
    row["outcome"] = "match" if done == N and r["counted"] == N else "mismatch"
    if name == "uprobe-grow" and not crash:
        # пролог растит стек при каждом вызове — 2 срабатывания на вызов; изредка ещё одно
        # (та же проверка в прологе служит кооперативному вытеснению) — точный счёт
        # от прогона к прогону плавает и в сравнение сессий не входит («~»)
        row["counted~"] = row.pop("counted")
        row["per_call"] = round(row["counted~"] / N)
        row["detail"] = "нагрузка %d, насчитано %d — по %d срабатывания на вызов" % (
            done, row["counted~"], row["per_call"])
    elif crash:
        row["detail"] = "нагрузка упала (%s): %s; насчитано %d" % (r["load_exit"], crash, r["counted"])
    if "detail" not in row:
        row["detail"] = "нагрузка %d, насчитано %d" % (done, r["counted"])
    return row


if __name__ == "__main__":
    common.run("hooks", measure, list(VARIANTS), HOOKS, END_MARKER)
