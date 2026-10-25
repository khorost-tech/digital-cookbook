"""Ось 1: разбор. Контроль первым.

Контроль: на правиле v1 обе схемы совпадают с эталоном во все дни — иначе
сравнение пересчёта ничего не значит.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import ELT  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "elt.jsonl")


def read(path=FIXTURE):
    return ELT.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["step"] == "initial" and r["outcome"] != "match":
            raise SystemExit("контроль %s: на правиле v1 не совпало с эталоном" % r["variant"])
        if r["matched_days"] + len(r["mismatch"]) != r["days"]:
            raise SystemExit("%s: счёт дней не сходится" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-18s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
