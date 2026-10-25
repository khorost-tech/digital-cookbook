"""Ось 9: разбор. Контроль первым: ответ 1 МБ доходит во всех парах."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import SIZE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "size.jsonl")


def read(path=FIXTURE):
    return SIZE.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["name"] == "1mb" and r["outcome"] != "delivered":
            raise SystemExit("контроль %s: 1 МБ не дошёл" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-24s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
