"""Ось 6: JSON-логи → fixtures/logs.jsonl. Одна строка — одна команда
`bash scripts/logs.sh <логгер> <сообщение>`; вывод хранится как есть (base64)."""

import base64
import importlib.util
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, read_measurements  # noqa: E402

_spec = importlib.util.spec_from_file_location(
    "analyze_logs", os.path.join(docker.HERE, "scripts", "analyze-logs.py"))
analyze = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(analyze)

FIXTURE = os.path.join(docker.HERE, "fixtures", "logs.jsonl")


def main():
    docker.build_base()
    rows = []
    for logger in ("pattern", "escaped"):
        for message in analyze.MESSAGES:
            argv = ["bash", "scripts/logs.sh", logger, message]
            p = subprocess.run(argv, cwd=docker.HERE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               timeout=600)
            if p.returncode != 0:
                raise SystemExit("%s: код %d\n%s" % (" ".join(argv), p.returncode, p.stderr.decode()))
            row = {"kind": "log", "variant": "%s/%s" % (logger, message), "cmd": " ".join(argv),
                   "logger": logger, "message": message,
                   "output_b64": base64.b64encode(p.stdout).decode(),
                   "output": p.stdout.decode("utf-8", "replace")}
            row.update(analyze.judge(p.stdout, message))
            rows.append(row)
            print("%-8s %-10s lines=%d parsed=%d utf8=%s round_trip=%s" % (
                logger, message, row["lines"], row["parsed"], row["valid_utf8"], row["round_trip"]))
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")
    read_measurements(FIXTURE)


if __name__ == "__main__":
    main()
