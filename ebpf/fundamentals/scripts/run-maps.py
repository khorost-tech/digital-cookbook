"""Ось 2: карты под конкуренцией (cmd/maps, bpf/maps.c).

N вызовов getppid из K потоков на разных CPU; одна программа на tracepoint
sys_enter_getppid пишет в карту:
  hash-racy    — hash, прочитать-прибавить-записать без атомарности;
  hash-atomic  — hash, __sync_fetch_and_add;
  percpu       — per-CPU массив, сумма по CPU (контроль);
  ring-small   — ring buffer 4 КБ, чтение после нагрузки;
  ring-read    — ring buffer 4 КБ, чтение во время нагрузки;
  ring-big     — ring buffer 32 МБ.
Эталон — счёт нагрузки. Величины, которые от прогона к прогону разные (счёт
гонки, получено при чтении на лету), помечены «~» и в сравнение сессий не входят;
публикуется устойчивое: есть ли потери, сходится ли учёт получено + отказов = N.

    python3 scripts/run-maps.py              → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, MAPS  # noqa: E402

N, K = 1000000, 8
VARIANTS = {"hash-racy": ("hash-racy",), "hash-atomic": ("hash-atomic",), "percpu": ("percpu",),
            "ring-small": ("ring", 4096), "ring-read": ("ring-read", 4096), "ring-big": ("ring", 33554432)}
UNSTABLE = ("hash-racy", "ring-read")


def measure(name):
    args = VARIANTS[name]
    r = stack.run("maps", args[0], N, K, *args[1:])
    row = {"probe": "burst %d events, %d threads" % (N, K), "n": N, "threads": K}
    if "error" in r:
        row.update({"outcome": "error", "detail": "не загрузилось: %s" % r["error"]})
        return row
    done, counted = r["load"]["done"], r["counted"]
    row["load_done"] = done
    if len(args) > 1:
        row["ring_bytes"] = args[1]
    lost = done - counted
    row["lost"] = lost > 0
    if name in UNSTABLE:
        row["counted~"], row["lost_count~"] = counted, lost
    else:
        row["counted"], row["lost_count"] = counted, lost
    if "dropped" in r:
        row["dropped" + ("~" if name in UNSTABLE else "")] = r["dropped"]
        row["accounted"] = counted + r["dropped"] == done
    if "cpus_used" in r:
        row["cpus_used"] = r["cpus_used"]
    row["outcome"] = "match" if counted == done == N else "mismatch"
    row["detail"] = "нагрузка %d, насчитано %d, потеряно %d" % (done, counted, lost)
    if "dropped" in r:
        row["detail"] += "; отказов reserve %d, получено + отказов %s нагрузке" % (
            r["dropped"], "=" if row["accounted"] else "≠")
    return row


if __name__ == "__main__":
    common.run("maps", measure, list(VARIANTS), MAPS, END_MARKER)
