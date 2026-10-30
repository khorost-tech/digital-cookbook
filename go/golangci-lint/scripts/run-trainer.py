"""Тренажёр: каждый уровень на старте красный, на эталоне зелёный.

Стенд зовёт грейдер тренажёра (trainer/check.py --json) так же, как читатель, —
на нетронутом уровне и на его эталоне из trainer/solutions/. Счёт: прошёл ли
уровень, какие замечания выдал, у каждого ли замечания есть подсказка.

    python3 scripts/run-trainer.py             → все варианты, фикстура
    python3 scripts/run-trainer.py l3 start    → один вариант, строка JSON
"""

import itertools
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, TRAINER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "trainer.jsonl")
TRAINER_DIR = os.path.join(docker.HERE, "trainer")
LEVELS = ("l1", "l2", "l3", "l4")
STATES = ("start", "solution")

with open(os.path.join(TRAINER_DIR, "hints.json"), encoding="utf-8") as _f:
    HINTS = json.load(_f)


def measure(level, state):
    argv = ["python3", os.path.join(TRAINER_DIR, "check.py"), level, "--json"]
    if state == "solution":
        argv += ["--dir", "solutions/" + level]
    p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=900)
    out = p.stdout.decode("utf-8", "replace").strip()
    row = {"kind": "trainer", "variant": "%s/%s" % (level, state), "probe": level,
           "cmd": "python3 scripts/run-trainer.py %s %s" % (level, state),
           "check_cmd": "./check.sh %s%s" % (level, " --dir solutions/" + level if state == "solution" else ""),
           "level": level, "state": state}
    try:
        r = json.loads(out.split("\n")[-1])
    except ValueError:
        row.update({"outcome": "error", "detail": "грейдер не дал JSON: %s" % (out + p.stderr.decode())[-300:]})
        TRAINER.parse_line(json.dumps(row, ensure_ascii=False))
        return row
    keys = sorted(x["key"] for x in r["problems"])
    row.update({"pass": r["pass"], "problems": keys, "no_hint": sorted({k for k in keys if k not in HINTS})})
    row["outcome"] = "green" if r["pass"] else "red"
    row["detail"] = "замечаний нет" if r["pass"] else "замечаний %d: %s" % (len(keys), ", ".join(keys))
    TRAINER.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    if sys.argv[1:]:
        print(json.dumps({k: measure(*sys.argv[1:3])[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.pull()
    rows = []
    for level, state in itertools.product(LEVELS, STATES):
        rows.append(measure(level, state))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
