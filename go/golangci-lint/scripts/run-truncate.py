"""Ось 3: что прячет вывод golangci-lint по умолчанию.

Пакеты под три обрезки: trunc/same — 5 находок с одинаковым текстом
(max-same-issues 3); trunc/local — 60 разных вызовов локальных функций, у которых
errcheck пишет один и тот же текст без имени функции (та же обрезка); trunc/many
— 60 находок unused с разным текстом (max-issues-per-linter 50); trunc/line — две
находки разных линтеров на одной строке (uniq-by-line). Каждый пакет — дважды: с обрезкой по умолчанию и без неё
(контроль: показаны все найденные).

    python3 scripts/run-truncate.py                    → все варианты, фикстура
    python3 scripts/run-truncate.py same default       → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, TRUNCATE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "truncate.jsonl")
PACKAGES = ("same", "local", "many", "line")
MODES = ("no-limits", "default")
CONFIG = ["-c", "configs/truncate.yml"]


def measure(pkg, mode):
    target = "./trunc/%s/..." % pkg
    issues, rc, _ = docker.lint(*CONFIG, target, limits=(mode == "no-limits"))
    real, _, _ = docker.lint(*CONFIG, target)  # всё, что линтер нашёл на самом деле
    argv = ["golangci-lint", "run", *CONFIG] + (list(docker.NO_LIMITS) if mode == "no-limits" else []) + [target]
    row = {"kind": "truncate", "variant": "%s/%s" % (pkg, mode), "probe": pkg,
           "cmd": "python3 scripts/run-truncate.py %s %s" % (pkg, mode), "lint_cmd": " ".join(argv),
           "package": pkg, "mode": mode, "rc": rc, "shown": len(issues), "found": len(real)}
    if len(real) == 0:
        row["outcome"], row["detail"] = "error", "линтер не нашёл ничего — опыт не поставлен"
    elif len(issues) == len(real):
        row["outcome"], row["detail"] = "all-shown", "показаны все %d находок" % len(real)
    else:
        row["outcome"] = "truncated"
        row["detail"] = "показано %d из %d найденных, код выхода %d" % (len(issues), len(real), rc)
    TRUNCATE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    if sys.argv[1:]:
        print(json.dumps({k: measure(*sys.argv[1:3])[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.pull()
    rows = []
    for pkg, mode in itertools.product(PACKAGES, MODES):
        rows.append(measure(pkg, mode))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
