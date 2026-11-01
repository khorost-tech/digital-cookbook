"""Ось 4: CO-RE (cmd/core, bpf/core.c, bpf/core_old.c).

  tgid    — task_struct->tgid через CO-RE против PID нагрузки (контроль);
  guarded — поле state (до 5.14) или __state (после): выбор при загрузке,
            bpf_core_field_exists; эталон — 0 (TASK_RUNNING);
  old     — программа под ядра до 5.14 читает state без проверки.

    python3 scripts/run-core.py              → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import CORE, END_MARKER  # noqa: E402

VARIANTS = ("tgid", "guarded", "old")


def measure(name):
    r = stack.run("core", name)
    row = {"probe": "getppid x100, %s" % name, "stage": r["stage"]}
    if r["stage"] == "setup":
        row.update({"outcome": "error", "detail": "не поставить опыт: %s" % r.get("error")})
        return row
    if r["stage"] != "loaded":
        reason = r["error"].split("load program: ", 1)[-1].split(" (")[0]
        row.update({"reason": reason, "outcome": "mismatch", "detail": "не загрузилась: %s" % reason})
        return row
    want = r["truth_tgid"] if name == "tgid" else 0
    row.update({"value_matches": r["value"] == want, "branch": r.get("branch")})
    row["outcome"] = "match" if r["value"] == want else "mismatch"
    row["detail"] = (("прочитанный tgid совпал с PID нагрузки" if r["value"] == want else "прочитано %d, PID нагрузки другой" % r["value"])
                     if name == "tgid" else
                     "ветка %s, прочитано %d, эталон 0 (TASK_RUNNING)" % (r["branch"], r["value"]))
    return row


if __name__ == "__main__":
    common.run("core", measure, VARIANTS, CORE, END_MARKER)
