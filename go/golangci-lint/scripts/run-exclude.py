"""Ось 4: что прячут исключения golangci-lint v2.

Контроль — конфиг без исключений (configs/exclude-base.yml): находит всё
размеченное в sample/ и excl/, и дефекты (`// want`), и шум (`// noise`).
Каждый вариант — тот же конфиг плюс одно исключение: пресет, правило по пути
для тестов, режим сгенерированного кода. Два последних — курируемый конфиг
целиком: прежний, с широкими исключениями (G107/G304 на весь проект, любой
Close через io.Closer), и узкий; excl/danger — те же правила на входе из запроса
и Close после записи, то есть настоящие дефекты. Счёт: какие размеченные находки
контроля пропали и были ли среди них дефекты, а не только шум.

    python3 scripts/run-exclude.py                       → все варианты, фикстура
    python3 scripts/run-exclude.py preset-legacy         → один вариант, строка JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker, marks  # noqa: E402
from scripts.outcome import END_MARKER, EXCLUDE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "exclude.jsonl")
VARIANTS = ("base", "preset-comments", "preset-std-error-handling", "preset-common-false-positives",
            "preset-legacy", "path-tests", "generated-lax", "generated-strict", "curated-broad", "curated")
TARGETS = ("./sample/...", "./excl/...")


def marked():
    want = marks.collect("sample") | marks.collect("excl")
    noise = marks.collect("sample", tag="noise") | marks.collect("excl", tag="noise")
    return want, noise


def measure(name):
    cfg = "configs/exclude-%s.yml" % name
    issues, rc, _ = docker.lint("-c", cfg, *TARGETS)
    want, noise = marked()
    s = marks.score(issues, want, noise)
    row = {"kind": "exclude", "variant": name, "probe": "sample+excl",
           "cmd": "python3 scripts/run-exclude.py %s" % name,
           "lint_cmd": " ".join(["golangci-lint", "run", "-c", cfg, *docker.NO_LIMITS, *TARGETS]),
           "issues": len(issues), "found": len(s["found"]), "noise": len(s["noise"]), "extra": len(s["extra"]),
           "hidden_want": ["%s:%d %s" % m for m in s["missed"]],
           "hidden_noise": ["%s:%d %s" % m for m in sorted(noise - set(s["noise"]))]}
    hidden = len(row["hidden_want"]) + len(row["hidden_noise"])
    if s["extra"]:
        row["outcome"], row["detail"] = "error", "находки вне разметки: %s" % s["extra"][:3]
    elif name == "base":
        row["outcome"] = "nothing-hidden" if not hidden else "error"
        row["detail"] = "без исключений: найдено всё размеченное (%d дефектов, %d шума)" % (
            row["found"], row["noise"]) if not hidden else "контроль сломан: спрятано %d" % hidden
    elif hidden:
        row["outcome"] = "hidden"
        row["detail"] = "спрятано: дефектов %d, шума %d" % (len(row["hidden_want"]), len(row["hidden_noise"]))
    else:
        row["outcome"], row["detail"] = "nothing-hidden", "на этом коде не спрятано ничего"
    EXCLUDE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    if sys.argv[1:]:
        print(json.dumps({k: measure(sys.argv[1])[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.pull()
    rows = []
    for name in VARIANTS:
        rows.append(measure(name))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
