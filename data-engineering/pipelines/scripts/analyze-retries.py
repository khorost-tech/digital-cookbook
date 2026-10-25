"""Ось 6: разбор. Контроль первым.

Контроль: задача без сбоев — успех с первой попытки и эталон. Повторы делает
Airflow: число попыток = min(падений + 1, retries + 1).
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import RETRIES  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "retries.jsonl")


def read(path=FIXTURE):
    return RETRIES.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["try_number"] != min(r["fail_times"] + 1, r["retries"] + 1):
            raise SystemExit("%s: попыток %d — повторы делал не Airflow" % (r["variant"], r["try_number"]))
    c = by["control"]
    if (c["task_state"], c["try_number"], c["outcome"]) != ("success", 1, "match"):
        raise SystemExit("контроль: задача без сбоев не дала эталон с первой попытки")


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-20s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
