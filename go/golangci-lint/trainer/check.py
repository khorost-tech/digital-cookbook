#!/usr/bin/env python3
"""Грейдер тренажёра golangci-lint: ./check.sh l1 … l4.

Запускает линтер (L1–L3) или analysistest (L4) в образе golangci-lint той же
версии, что у стенда, и печатает PASS или FAIL с подсказкой на каждое замечание.

    ./check.sh l2                       проверить уровень
    ./check.sh l2 --dir solutions/l2    проверить эталон
    ./check.sh l2 --json                итог одной строкой JSON (для стенда)
"""

import argparse
import json
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(HERE, "..", "images.json"), encoding="utf-8") as f:
    _img = json.load(f)["golangci-lint"]
IMAGE = "%s@%s" % (_img["ref"], _img["digest"])
with open(os.path.join(HERE, "hints.json"), encoding="utf-8") as f:
    HINTS = json.load(f)
LEVELS = ("l1", "l2", "l3", "l4")
CACHE = "golangci-stand-cache"


def docker(*argv):
    p = subprocess.run(["docker", "run", "--rm", "-v", "%s:/src" % HERE, "-w", "/src",
                        "-v", "%s:/root/.cache" % CACHE, "-v", "%s-mod:/go/pkg/mod" % CACHE, IMAGE, *argv],
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    return p.returncode, p.stdout.decode("utf-8", "replace"), p.stderr.decode("utf-8", "replace")


def hint_key(linter, text):
    m = re.match(r"(G\d+):", text)
    if linter == "gosec" and m:
        return "gosec:" + m.group(1)
    m = re.match(r"(\w+):", text)
    if linter == "govet" and m:
        return "govet:" + m.group(1)
    return linter


def lint(d):
    rc, out, err = docker("golangci-lint", "run", "-c", "%s/.golangci.yml" % d, "--max-same-issues", "0",
                          "--max-issues-per-linter", "0", "--uniq-by-line=false",
                          "--output.json.path", "stdout", "--output.text.path", "stderr", "./%s/..." % d)
    if rc not in (0, 1):
        raise SystemExit("golangci-lint не запустился (код %d): %s" % (rc, (err or out)[-800:]))
    report = json.loads(out.split("\n", 1)[0])
    return [{"where": "%s:%d" % (i["Pos"]["Filename"], i["Pos"]["Line"]), "key": hint_key(i["FromLinter"], i["Text"]),
             "text": i["Text"]} for i in (report.get("Issues") or [])]


def level3_extra(d):
    """L3: в конфиге включён sqlclosecheck; строка под меткой подавлена узко и с причиной."""
    problems = []
    with open(os.path.join(HERE, d, ".golangci.yml"), encoding="utf-8") as f:
        if not re.search(r"\bsqlclosecheck\b", f.read()):
            problems.append({"where": "%s/.golangci.yml" % d, "key": "l3:no-linter", "text": "линтер не включён"})
    with open(os.path.join(HERE, d, "api.go"), encoding="utf-8") as f:
        lines = f.read().split("\n")
    for n, line in enumerate(lines):
        if "L3: подавить обоснованно" in line:
            target = lines[n + 1] if n + 1 < len(lines) else ""
            if not re.search(r"//nolint:errcheck // \S", target):
                problems.append({"where": "%s/api.go:%d" % (d, n + 2), "key": "l3:no-nolint",
                                 "text": "нет узкой директивы с причиной"})
    return problems


def check(level, d):
    if level == "l4":
        rc, out, err = docker("go", "test", "-count=1", "./%s/..." % d)
        problems = [] if rc == 0 else [{"where": d, "key": "l4:test",
                                        "text": " ".join(l.strip() for l in (out + err).split("\n")
                                                         if "analysistest" in l)[:300]}]
        return problems
    problems = lint(d)
    if level == "l3":
        problems += level3_extra(d)
    return problems


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("level", choices=LEVELS)
    ap.add_argument("--dir")
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args()
    d = a.dir or a.level
    problems = check(a.level, d)
    if a.json:
        print(json.dumps({"level": a.level, "dir": d, "pass": not problems,
                          "problems": problems}, ensure_ascii=False))
        return 0 if not problems else 1
    if not problems:
        print("PASS %s: замечаний нет" % d)
        nxt = LEVELS.index(a.level) + 1
        if nxt < len(LEVELS) and not a.dir:
            print("Дальше: ./check.sh %s" % LEVELS[nxt])
        return 0
    print("FAIL %s: замечаний %d" % (d, len(problems)))
    for p in problems:
        print("\n  %s  [%s]\n    %s\n    → %s" % (p["where"], p["key"], p["text"],
                                              HINTS.get(p["key"], "подсказки нет — смотрите документацию линтера")))
    return 1


if __name__ == "__main__":
    sys.exit(main())
