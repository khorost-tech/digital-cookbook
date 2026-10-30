"""Ось 7: своё правило ctxloss против готовых линтеров про контекст.

ctxloss (analyzer/ctxloss) сообщает о context.Background()/TODO() в функции, у
которой уже есть параметр context.Context. Варианты:
  analysistest — `go test` анализатора: положительные и отрицательные случаи;
  custom       — свой бинарь `golangci-lint custom` с плагином: ctxloss,
                 contextcheck и noctx на ctx/;
  builtin      — обычный бинарь: contextcheck и noctx на ctx/;
  plain-binary — обычный бинарь с конфигом, где включён ctxloss (контроль: он
                 его не знает и обязан упасть).
Счёт — по функциям, а не по строкам: contextcheck ставит находку туда, где
контекст использован, а не где создан. Функция с меткой `// want ctxloss` —
теряет срок; без метки — не теряет.

    python3 scripts/run-custom.py              → все варианты, фикстура
    python3 scripts/run-custom.py custom       → один вариант, строка JSON
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker, marks  # noqa: E402
from scripts.outcome import CUSTOM, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "custom.jsonl")
SOURCE = "ctx/handler/handler.go"
CUSTOM_BIN = "/src/bin/golangci-lint-ctx"
VARIANTS = ("analysistest", "custom", "builtin", "plain-binary")
LINTERS = {"custom": ("ctxloss", "contextcheck", "noctx"), "builtin": ("contextcheck", "noctx")}


def functions():
    """[(имя, первая строка, последняя строка)] функций файла."""
    with open(os.path.join(docker.HERE, SOURCE), encoding="utf-8") as f:
        lines = f.read().split("\n")
    starts = [(n, re.match(r"func (?:\([^)]*\) )?(\w+)", l).group(1)) for n, l in enumerate(lines, 1)
              if l.startswith("func ")]
    out = []
    for k, (n, name) in enumerate(starts):
        end = starts[k + 1][0] - 1 if k + 1 < len(starts) else len(lines)
        out.append((name, n, end))
    return out


def func_of(line, funcs):
    return next((name for name, a, b in funcs if a <= line <= b), None)


def build():
    rc, out, err = docker.run_in(docker.HERE, "custom", "-v", timeout=1800)
    version = re.search(r"version: (\S+)", out + err)
    return rc, version.group(1).rstrip('"') if version else ""


def score(issues, linters):
    funcs = functions()
    lossy = {func_of(m[1], funcs) for m in marks.collect("ctx") if m[0] == SOURCE}
    clean = {name for name, _, _ in funcs} - lossy
    out = {}
    for linter in linters:
        hit = {func_of(i["line"], funcs) for i in issues if i["linter"] == linter and i["file"] == SOURCE}
        out[linter] = {"caught": sorted(hit & lossy), "missed": sorted(lossy - hit), "false": sorted(hit & clean)}
    return sorted(lossy), out


def base(name):
    return {"kind": "custom", "variant": name, "probe": SOURCE if name != "analysistest" else "analyzer/ctxloss",
            "cmd": "python3 scripts/run-custom.py %s" % name}


def measure(name):
    row = base(name)
    if name == "analysistest":
        rc, out, err = docker.run_in(os.path.join(docker.HERE, "analyzer"), "test", "-count=1", "./...", binary="go")
        row.update({"lint_cmd": "go test ./... (в analyzer/)", "rc": rc})
        row["outcome"], row["detail"] = ("scored", "analysistest прошёл") if rc == 0 else \
            ("error", "analysistest упал: %s" % (out + err)[-300:])
    elif name == "plain-binary":
        rc, out, err = docker.run_in(docker.HERE, "run", "-c", "configs/custom.yml", "./ctx/...")
        msg = re.search(r'plugin "ctxloss" not found', out + err)
        row.update({"lint_cmd": "golangci-lint run -c configs/custom.yml ./ctx/...", "rc": rc,
                    "error_text": msg.group(0) if msg else ""})
        row["outcome"], row["detail"] = ("scored", "обычный бинарь отказался: %s (код %d)" % (msg.group(0), rc)) \
            if msg and rc not in (0, 1) else ("error", "обычный бинарь не отказался: rc=%d" % rc)
    else:
        binary, cfg = (CUSTOM_BIN, "configs/custom.yml") if name == "custom" else \
            ("golangci-lint", "configs/ctx-builtin.yml")
        if name == "custom":
            rc, version = build()
            if rc != 0:
                row.update({"outcome": "error", "detail": "сборка своего бинаря упала"})
                CUSTOM.parse_line(json.dumps(row, ensure_ascii=False))
                return row
            row["custom_version"] = version
        issues, rc, _ = docker.lint("-c", cfg, "./ctx/...", binary=binary)
        lossy, by = score(issues, LINTERS[name])
        row.update({"lint_cmd": " ".join([os.path.basename(binary), "run", "-c", cfg, *docker.NO_LIMITS, "./ctx/..."]),
                    "rc": rc, "lossy": lossy, "linters": by})
        row["outcome"] = "scored"
        row["detail"] = "; ".join("%s: поймано %d из %d, зря %d" % (l, len(v["caught"]), len(lossy), len(v["false"]))
                                  for l, v in by.items())
    CUSTOM.parse_line(json.dumps(row, ensure_ascii=False))
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
