"""Ось 1: засеянные дефекты против детекторов → fixtures/defects.jsonl.

Каждая строка — одна команда `bash scripts/detect.sh <детектор> <уровень>
<программа>`, её код возврата снят отдельно, вывод хранится целиком.
`defect_present` — по дизассемблеру того же бинарника, который получил
детектор (`detect.sh … --disasm`, scripts/presence.py).
"""

import importlib.util
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import DEFECTS, END_MARKER  # noqa: E402
from scripts.presence import defect_present  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "analyze_defects", os.path.join(docker.HERE, "scripts", "analyze-defects.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

FIXTURE = os.path.join(docker.HERE, "fixtures", "defects.jsonl")
PROGRAMS = ["clean", "heap_overflow", "use_after_free", "signed_overflow",
            "data_race", "leak", "uninit_read", "parser_edge"]
DETECTORS = ["plain", "gtest", "asan", "ubsan", "tsan", "valgrind"]
# Клетки, повторяемые несколько раз: исход зависит от планирования потоков.
# Недетерминированность предсказана до прогона (expected.json, defects).
REPEATS = {("data_race", "gtest"): 5}
COMPILER = {"plain": "g++", "gtest": "g++", "valgrind": "g++",
            "asan": "clang++", "ubsan": "clang++", "tsan": "clang++", "libfuzzer": "clang++"}


def disassembly(detector, opt, program):
    """Дизассемблер ТОГО ЖЕ бинарника, который получает детектор (detect.sh --disasm)."""
    argv = ["bash", "scripts/detect.sh", detector, opt, program, "--disasm"]
    proc = subprocess.run(argv, cwd=docker.HERE, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=600)
    if proc.returncode != 0:
        raise SystemExit("--disasm %s %s %s: код %d" % (detector, opt, program, proc.returncode))
    return proc.stdout.decode("utf-8", "replace")


def detect(detector, opt, program):
    argv = ["bash", "scripts/detect.sh", detector, opt, program]
    proc = subprocess.run(argv, cwd=docker.HERE, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=600)
    return proc.returncode, proc.stdout.decode("utf-8", "replace"), " ".join(argv)


def main():
    docker.build_base()
    plan = [(p, d, o) for p in PROGRAMS for d in DETECTORS for o in ("O0", "O2")]
    plan += [("parser_edge", "libfuzzer", o) for o in ("O0", "O2")]
    rows = []
    for program, detector, opt in plan:
        n = REPEATS.get((program, detector), 1)
        runs = []
        for _ in range(n):
            rc, out, cmd = detect(detector, opt, program)
            runs.append((rc, out, analyze.classify(rc, out, analyze.RAN in out, detector=detector)))
        caught = [r for r in runs if r[2]["outcome"] == "caught"]
        # Вывод — первого поймавшего повтора, если такой был: листинг статьи
        # показывает, как тест ловит; счёт — в caught_in.
        rc, out, verdict = (caught or runs)[0]
        broken = [r for r in runs if r[2]["outcome"] == "error"]
        if broken:                                   # сломанная проба не прячется в счёте
            rc, out, verdict = broken[0]
        elif n > 1 and 0 < len(caught) < n:
            verdict = {"outcome": "mixed", "detail": "поймал в %d из %d повторов" % (len(caught), n)}
        present = defect_present(program, disassembly(detector, opt, program))
        row = {"kind": "defect", "variant": program, "probe": "%s-%s" % (detector, opt),
               "outcome": verdict["outcome"], "detail": verdict["detail"], "cmd": cmd,
               "program": program, "detector": detector, "opt": opt,
               "compiler": COMPILER[detector], "defect_present": present, "rc": rc,
               # Вывод целиком: итоговая строка отчёта (SUMMARY) стоит в середине,
               # над картой теневой памяти, и обрезанный хвост её терял.
               "output": out}
        if n > 1:
            row.update({"repeats": n, "caught_in": len(caught), "nondeterministic": True,
                        "errors_in": sum(1 for r in runs if r[2]["outcome"] == "error")})
        DEFECTS.parse_line(json.dumps(row, ensure_ascii=False))
        rows.append(row)
        print("%-16s %-9s %s %-7s present=%-5s rc=%-3s %s" % (
            program, detector, opt, verdict["outcome"], present, rc, verdict["detail"][:60]))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
