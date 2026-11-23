"""Ось 7: покрытие → fixtures/coverage.jsonl.

Строки: coverage/on — сводка (export), разметка (show), зелёный ли основной
набор; boundary/on и boundary/off — контрольный тест на 65536.
"""

import json
import os
import re
import subprocess
import sys
import importlib.util

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, read_measurements  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "analyze_coverage", os.path.join(docker.HERE, "scripts", "analyze-coverage.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

FIXTURE = os.path.join(docker.HERE, "fixtures", "coverage.jsonl")


def run(*args):
    argv = ["bash", "scripts/coverage.sh"] + list(args)
    p = subprocess.run(argv, cwd=docker.HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=900)
    return p.returncode, p.stdout.decode("utf-8", "replace"), " ".join(argv)


def main():
    docker.build_base()
    rows = []
    rc_e, export, cmd_e = run("on", "export")
    rc_s, show, cmd_s = run("on", "show")
    rc_r, report, cmd_r = run("on", "report")
    if rc_e or rc_s:
        raise SystemExit("покрытие: коды %d %d\n%s" % (rc_e, rc_s, export[-2000:]))
    uncovered = [l.strip() for l in show.splitlines()
                 if re.search(r"Branch \(.*(True: 0|False: 0)\]", l) or "Pair: not covered" in l]
    rows.append({"kind": "coverage", "variant": "on", "planted": "on", "cmd": cmd_r,
                 "cmd_export": cmd_e, "cmd_show": cmd_s, "report": report, "show": show,
                 "export": export, "summary": analyze.summary(export),
                 # coverage.sh запускает основной набор перед отчётом и
                 # обрывает цепочку, если набор упал: код 0 — набор зелёный.
                 "suite_rc": rc_r, "suite_passed": rc_r == 0, "uncovered": uncovered})
    for planted in ("on", "off"):
        rc, out, cmd = run(planted, "boundary")
        rows.append({"kind": "boundary", "variant": planted, "planted": planted, "cmd": cmd,
                     "rc": rc, "boundary_passed": rc == 0 and "PASSED" in out, "output": out[-1500:]})
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")
    read_measurements(FIXTURE)
    print(json.dumps(rows[0]["summary"]), rows[0]["uncovered"])
    print("boundary:", [(r["planted"], r["boundary_passed"]) for r in rows[1:]])


if __name__ == "__main__":
    main()
