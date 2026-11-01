"""Ось 3: verifier (cmd/verifier, bpf/verifier/*.c).

У каждой программы ожидаемый вердикт в первой строке файла — гипотеза,
записанная до пробы. Загрузчик компилирует программу и пробует загрузить:
стадия (clang, verifier, accepted) и строка-причина отказа. Контроль — ok.c
принята.

    python3 scripts/run-verifier.py              → все программы, фикстура
    python3 scripts/run-verifier.py null_deref   → одна программа
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, VERIFIER  # noqa: E402

PROGRAMS = ("ok", "null_deref", "null_deref_hash", "loop_bounded", "loop_collapsed", "loop_unbounded", "bpf_loop",
            "packet_oob", "packet_checked", "stack_uninit", "stack_big", "gpl_helper")
STAGE = {"accepted": "accepted", "rejected": "verifier", "clang": "clang"}


def measure(name):
    r = stack.run("verifier", name)
    row = {"probe": "bpf/verifier/%s.c" % name, "expect": r["expect"], "stage": r["stage"],
           "reason": r.get("reason")}
    if r["stage"] == "setup":
        row.update({"outcome": "error", "detail": "не поставить опыт: %s" % r.get("reason")})
        return row
    row["outcome"] = "match" if STAGE[r["expect"]] == r["stage"] else "mismatch"
    verdict = {"accepted": "принята", "verifier": "отклонена verifier", "clang": "отклонена clang"}[r["stage"]]
    row["detail"] = verdict + (": %s" % r["reason"] if r.get("reason") else "")
    if "run" in r:  # загрузилась — отдельно: выполнила ли заданное число итераций
        run = r["run"]
        row.update({"run_n": run.get("n"), "run_iterations": run.get("iterations"), "run_ret": run.get("ret"),
                    "run_complete": run.get("iterations") == run.get("n") and run.get("ret") == run.get("n")})
        row["detail"] += "; исполнение: n = %d, итераций %d, bpf_loop вернул %d" % (
            run["n"], run["iterations"], run["ret"])
    return row


if __name__ == "__main__":
    common.run("verifier", measure, PROGRAMS, VERIFIER, END_MARKER)
