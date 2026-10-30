"""Ось 6: разбор. Контроль первым.

Контроль: полный прогон показывает все размеченные дефекты репозитория.
Хеши коммитов одинаковы во всех строках — все прогоны шли по одному диапазону.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import LEGACY  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "legacy.jsonl")


def read(path=FIXTURE):
    return LEGACY.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if len(by["full"]["shown"]) != by["full"]["want"]:
        raise SystemExit("контроль: полный прогон показал не всё")
    if len({(r["main"], r["feature"]) for r in rows}) != 1:
        raise SystemExit("прогоны шли по разным коммитам")
    for r in rows:
        if not set(r["shown"]) <= set(by["full"]["shown"]):
            raise SystemExit("%s: показано то, чего нет в полном прогоне" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-20s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
