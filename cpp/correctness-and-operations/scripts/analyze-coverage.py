"""Ось 7: покрытие строк 100%, ошибка на месте. Контроль первым.

Проценты — из `llvm-cov export -summary-only` (JSON), а не из текстового
отчёта. Какое условие не покрыто — из `llvm-cov show` (строки Branch и пары
MC/DC), это поле uncovered.

Контроль: тест на 65536 падает при внесённой ошибке и проходит без неё —
иначе «ошибка не найдена» ничего бы не значило.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_measurements  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "coverage.jsonl")


def read(path=FIXTURE):
    return read_measurements(path)


def summary(export_json):
    """Проценты по файлу из llvm-cov export -summary-only."""
    data = json.loads(export_json)
    f = data["data"][0]["files"][0]["summary"]
    return {"lines": f["lines"]["percent"], "branches": f["branches"]["percent"],
            "mcdc": f["mcdc"]["percent"], "regions": f["regions"]["percent"]}


def pick(rows, kind, planted):
    found = [r for r in rows if r["kind"] == kind and r["planted"] == planted]
    if len(found) != 1:
        raise SystemExit("%s/%s: строк %d" % (kind, planted, len(found)))
    return found[0]


def check_controls(rows):
    on, off = pick(rows, "boundary", "on"), pick(rows, "boundary", "off")
    if on["boundary_passed"] or not off["boundary_passed"]:
        raise SystemExit("контроль: тест на 65536 не различает варианты (on=%s, off=%s)"
                         % (on["boundary_passed"], off["boundary_passed"]))


def main():
    rows = read()
    check_controls(rows)
    cov = pick(rows, "coverage", "on")
    print(json.dumps({"summary": cov["summary"], "suite_passed": cov["suite_passed"],
                      "uncovered": cov["uncovered"]}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
