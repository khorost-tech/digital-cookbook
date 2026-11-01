"""Ось 5: те же замеры однострочниками bpftrace (cmd/bt).

  open   — tracepoint sys_enter_openat, N открытий (контроль);
  burst  — tracepoint sys_enter_getppid, N вызовов из K потоков;
  uprobe — uprobe на main.targetFunc, N вызовов.
Эталон — счёт нагрузки.

    python3 scripts/run-bpftrace.py          → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import BPFTRACE, END_MARKER  # noqa: E402

VARIANTS = {"open": ("open", "open", 10000), "burst": ("burst", "burst", 1000000, 8),
            "uprobe": ("uprobe", "call", 10000)}


def measure(name):
    script, mode, n, *k = VARIANTS[name]
    r = stack.run("bt", script, mode, n, *k)
    row = {"probe": "bpftrace %s, %s %d" % (script, mode, n), "n": n, "program": r.get("program")}
    if "error" in r:
        row.update({"outcome": "error", "detail": r["error"][:200]})
        return row
    row.update({"counted": r["counted"], "load_done": r["load"]["done"]})
    row["outcome"] = "match" if r["counted"] == r["load"]["done"] == n else "mismatch"
    row["detail"] = "нагрузка %d, насчитано %d" % (r["load"]["done"], r["counted"])
    return row


if __name__ == "__main__":
    common.run("bpftrace", measure, list(VARIANTS), BPFTRACE, END_MARKER)
