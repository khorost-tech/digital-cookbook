"""Ось 5: что делает каждая форма //nolint и что из этого видит nolintlint.

nolint/cases/cases.go — шесть случаев, каждый под меткой `// case <имя>`, дефекты
размечены строкой выше (`// want-next`). Три прогона: контроль — копия кода без
директив во временном модуле (все дефекты обязаны найтись); plain — errcheck и
noctx без проверки директив; strict — то же с nolintlint (require-specific,
require-explanation, allow-unused: false). Счёт по случаю: какие размеченные
дефекты показаны, какие подавлены, что сказал nolintlint.

    python3 scripts/run-nolint.py                  → все варианты, фикстура
    python3 scripts/run-nolint.py strict two-bare  → один вариант, строка JSON
"""

import json
import os
import re
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker, marks  # noqa: E402
from scripts.outcome import END_MARKER, NOLINT  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "nolint.jsonl")
SOURCE = "nolint/cases/cases.go"
CONFIGS = ("control", "plain", "strict")
DIRECTIVE = re.compile(r"\s*//nolint.*$")


def cases():
    """{случай: строка с кодом и директивой}."""
    out, current = {}, None
    with open(os.path.join(docker.HERE, SOURCE), encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            m = re.match(r"\s*// case (\S+)", line)
            if m:
                current = m.group(1)
            elif current and not line.strip().startswith("//"):
                out[current], current = n, None
    return out


def lint(config):
    if config != "control":
        issues, _, _ = docker.lint("-c", "configs/nolint-%s.yml" % config, "./nolint/...")
        return issues
    # контроль: тот же код без директив — номера строк не меняются
    tmp = tempfile.mkdtemp(prefix="nolint-control-", dir=os.path.join(docker.HERE, "fixtures"))
    try:
        os.makedirs(os.path.join(tmp, "nolint", "cases"))
        shutil.copy(os.path.join(docker.HERE, "go.mod"), tmp)
        shutil.copytree(os.path.join(docker.HERE, "configs"), os.path.join(tmp, "configs"))
        with open(os.path.join(docker.HERE, SOURCE), encoding="utf-8") as f:
            text = "".join(DIRECTIVE.sub("", l.rstrip("\n")) + "\n" for l in f)
        with open(os.path.join(tmp, SOURCE), "w", encoding="utf-8") as f:
            f.write(text)
        issues, _, _ = docker.lint("-c", "configs/nolint-plain.yml", "./nolint/...", mount=tmp)
        return issues
    finally:
        shutil.rmtree(tmp)


def lintlint(text):
    if "should mention specific linter" in text:
        return "без имени линтера"
    if "should provide explanation" in text:
        return "без причины"
    if "is unused for linter" in text:
        return "не используется"
    return text[:60]


def row_for(config, case, line, issues, want):
    on_line = [i for i in issues if i["line"] == line and i["file"] == SOURCE]
    wanted = sorted(m[2] for m in want if m[1] == line)
    shown = sorted(i["linter"] for i in on_line if i["linter"] != "nolintlint")
    row = {"kind": "nolint", "variant": "%s/%s" % (config, case), "probe": case,
           "cmd": "python3 scripts/run-nolint.py %s %s" % (config, case),
           "lint_cmd": "golangci-lint run -c configs/nolint-%s.yml %s ./nolint/..." % (
               "plain" if config == "control" else config, " ".join(docker.NO_LIMITS)),
           "config": config, "case": case, "line": line, "want": wanted, "shown": shown,
           "suppressed": sorted(set(wanted) - set(shown)),
           "nolintlint": sorted(lintlint(i["text"]) for i in on_line if i["linter"] == "nolintlint")}
    if config == "control" and row["suppressed"]:
        row["outcome"], row["detail"] = "error", "контроль: без директив не найдено %s" % row["suppressed"]
    else:
        row["outcome"] = "scored"
        row["detail"] = "показано %s, подавлено %s, nolintlint: %s" % (
            shown or "—", row["suppressed"] or "—", row["nolintlint"] or "—")
    NOLINT.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def measure(config, case=None):
    issues, want, lines = lint(config), marks.collect("nolint"), cases()
    return [row_for(config, c, l, issues, want) for c, l in lines.items() if case in (None, c)]


SHOW = ("variant", "outcome", "detail")


def main():
    if sys.argv[1:]:
        for row in measure(*sys.argv[1:3]):
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.pull()
    rows = []
    for config in CONFIGS:
        for row in measure(config):
            rows.append(row)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
