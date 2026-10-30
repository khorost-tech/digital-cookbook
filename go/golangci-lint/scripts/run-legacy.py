"""Ось 6: что видит golangci-lint в легаси при --new-from-*.

legacy/build-repo.sh собирает во временном каталоге git-репозиторий: main со
старым кодом (три дефекта в двух файлах) и ветку feature — правка строки одного
старого дефекта и новая функция с новым дефектом; второй файл не тронут.
Прогоны на feature: полный (контроль — все размеченные дефекты),
--new-from-rev=HEAD~1, --new-from-merge-base=main, --new-from-rev=HEAD~1
--whole-files. Счёт: какие размеченные дефекты показаны.

    python3 scripts/run-legacy.py                  → все варианты, фикстура
    python3 scripts/run-legacy.py new-from-rev     → один вариант, строка JSON
"""

import json
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker, marks  # noqa: E402
from scripts.outcome import END_MARKER, LEGACY  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "legacy.jsonl")
VARIANTS = {
    "full": [],
    "new-from-rev": ["--new-from-rev=HEAD~1"],
    "new-from-merge-base": ["--new-from-merge-base=main"],
    "whole-files": ["--new-from-rev=HEAD~1", "--whole-files"],
}


def build():
    tmp = tempfile.mkdtemp(prefix="legacy-repo-", dir=os.path.join(docker.HERE, "fixtures"))
    shutil.copy(os.path.join(docker.HERE, "legacy", "build-repo.sh"), tmp)
    rc, out, err = docker.run_in(tmp, "sh", "-c", "sh build-repo.sh /src/repo >/src/heads 2>&1; echo rc=$?",
                                 binary="env")
    with open(os.path.join(tmp, "heads"), encoding="utf-8") as f:
        heads = f.read().split()
    if "rc=0" not in out or len(heads) != 2:
        shutil.rmtree(tmp)
        raise SystemExit("репозиторий не собран: %s %s %s" % (out, err, heads))
    return tmp, heads


def measure(name, repo=None):
    own = repo is None
    tmp, heads = build() if own else repo
    try:
        issues, rc, _ = docker.lint(*VARIANTS[name], "./...", mount=os.path.join(tmp, "repo"))
        want = marks.collect("legacy", base=os.path.join(tmp, "repo"))
    finally:
        if own:
            shutil.rmtree(tmp)
    s = marks.score(issues, want)
    shown = ["%s:%d" % (f, n) for f, n, _ in s["found"]]
    row = {"kind": "legacy", "variant": name, "probe": "main..feature",
           "cmd": "python3 scripts/run-legacy.py %s" % name,
           "lint_cmd": " ".join(["golangci-lint", "run", *VARIANTS[name], *docker.NO_LIMITS, "./..."]),
           "main": heads[0], "feature": heads[1], "want": len(want), "shown": shown, "extra": len(s["extra"])}
    if s["extra"]:
        row["outcome"], row["detail"] = "error", "находки вне разметки: %s" % s["extra"]
    elif name == "full" and s["missed"]:
        row["outcome"], row["detail"] = "error", "контроль: полный прогон пропустил %s" % s["missed"]
    else:
        row["outcome"], row["detail"] = "scored", "показано %d из %d: %s" % (len(shown), len(want), ", ".join(shown))
    LEGACY.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    if sys.argv[1:]:
        print(json.dumps({k: measure(sys.argv[1])[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.pull()
    repo = build()
    rows = []
    try:
        for name in VARIANTS:
            rows.append(measure(name, repo))
            print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    finally:
        shutil.rmtree(repo[0])
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
