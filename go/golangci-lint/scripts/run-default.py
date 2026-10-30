"""Оси 1–2: что находят набор по умолчанию, курируемый конфиг и «включить всё».

Каждый прогон — на sample/, вывод JSON, без обрезки (лимиты 0, uniq-by-line
выключен). Находки сверяются с разметкой: `// want` — ожидаемая диагностика
(пара место + линтер; на одном месте их может быть две), `// noise` — шум на
исправном по смыслу коде. Считаются две величины: диагностики и места; место
найдено, если сработал хоть один ожидаемый на нём линтер. Класс места — метка
`// kind`: dead — мёртвый код, opt — предложение оптимизации (не доказывает,
что программа работает неверно); без метки — ошибка относительно контракта,
записанного в комментарии функции. Контроль — fixed/ под курируемым
конфигом: ноль находок.

    python3 scripts/run-default.py              → все варианты, фикстура
    python3 scripts/run-default.py curated      → один вариант, строка JSON
"""

import collections
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker, marks  # noqa: E402
from scripts.outcome import DEFAULT, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "default.jsonl")
# вариант → (аргументы конфига, пакеты)
VARIANTS = {
    "standard": (["--no-config"], "./sample/..."),
    "curated": (["-c", "configs/curated.yml"], "./sample/..."),
    "all": (["--no-config", "--default", "all"], "./sample/..."),
    "fixed-curated": (["-c", "configs/curated.yml"], "./fixed/..."),
}


def lint_cmd(args, pkgs):
    return " ".join(["golangci-lint", "run", *args, *docker.NO_LIMITS, pkgs])


def measure(name):
    args, pkgs = VARIANTS[name]
    issues, rc, _ = docker.lint(*args, pkgs)
    root = pkgs[2:].split("/")[0]
    want = marks.collect(root)
    s = marks.score(issues, want, marks.collect(root, tag="noise"))
    places = {}
    for f, n, linter in want:
        places.setdefault((f, n), set()).add(linter)
    hit = {(f, n) for f, n, _ in s["found"]}
    kind = marks.kinds(root)
    by_kind = {k: {p for p in places if kind.get(p, "bug") == k} for k in ("bug", "dead", "opt")}
    row = {"kind": "default", "variant": name, "probe": root, "cmd": "python3 scripts/run-default.py %s" % name,
           "lint_cmd": lint_cmd(args, pkgs), "rc": rc, "issues": len(issues),
           "want": len(s["found"]) + len(s["missed"]), **{k: len(v) for k, v in s.items()},
           "places": len(places), "places_found": len(hit & set(places)),
           **{"%s_places" % k: len(v) for k, v in by_kind.items()},
           **{"%s_found" % k: len(v & hit) for k, v in by_kind.items()},
           "missed_by_linter": dict(collections.Counter(m[2] for m in s["missed"])),
           "found_by_linter": dict(collections.Counter(m[2] for m in s["found"])),
           "extra_by_linter": dict(collections.Counter(m[2] for m in s["extra"]))}
    if name.startswith("fixed"):
        row["outcome"] = "clean" if not issues else "error"
        row["detail"] = "исправленные пары: находок нет" if not issues else \
            "контроль сломан: на исправленном коде %d находок" % len(issues)
    else:
        row["outcome"] = "scored"
        row["detail"] = "диагностик %d из %d, мест %d из %d, шум %d, вне разметки %d" % (
            row["found"], row["want"], row["places_found"], row["places"], row["noise"], row["extra"])
    DEFAULT.parse_line(json.dumps(row, ensure_ascii=False))
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
