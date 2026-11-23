"""Ось 2: осталась ли работа в теле бенчмарка → fixtures/bench.jsonl.

Пять записей × g++/clang++ × -O0/-O2/-O3; на каждую строку — одна команда
`bash scripts/disasm-bench.sh <компилятор> <уровень> <функция>`. Бенчмарк не
запускается вовсе.
"""

import importlib.util
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import BENCH, END_MARKER  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "analyze_bench", os.path.join(docker.HERE, "scripts", "analyze-bench.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

FIXTURE = os.path.join(docker.HERE, "fixtures", "bench.jsonl")
WRITINGS = ["naive", "do_not_optimize", "clobber", "opaque_input", "empty"]


def main():
    docker.build_base()
    rows = []
    for cc in ("g++", "clang++"):
        for opt in ("O0", "O2", "O3"):
            for w in WRITINGS:
                fn = "body_" + w
                argv = ["bash", "scripts/disasm-bench.sh", cc, opt, fn]
                proc = subprocess.run(argv, cwd=docker.HERE, stdout=subprocess.PIPE,
                                      stderr=subprocess.STDOUT, timeout=600)
                out = proc.stdout.decode("utf-8", "replace")
                verdict = analyze.judge(out if proc.returncode == 0 else "", fn)
                row = {"kind": "bench", "variant": w, "probe": "%s-%s" % (cc, opt),
                       "cmd": " ".join(argv), "writing": w, "compiler": cc, "opt": opt,
                       "rc": proc.returncode, "disassembly": out[-6000:]}
                row.update(verdict)
                BENCH.parse_line(json.dumps(row, ensure_ascii=False))
                rows.append(row)
                print("%-8s %s %-16s %-9s %s" % (cc, opt, w, verdict["outcome"], verdict["detail"]))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
