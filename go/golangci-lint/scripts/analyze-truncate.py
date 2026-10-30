"""Ось 3: разбор. Контроль первым.

Контроль: без лимитов показано всё найденное. Вариант по умолчанию обязан
показать меньше — иначе опыт не отличает обрезку от её отсутствия.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import TRUNCATE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "truncate.jsonl")


def read(path=FIXTURE):
    return TRUNCATE.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["mode"] == "no-limits" and r["outcome"] != "all-shown":
            raise SystemExit("контроль %s: без лимитов показано не всё" % r["variant"])
        if r["mode"] == "default" and r["outcome"] != "truncated":
            raise SystemExit("%s: обрезка по умолчанию ничего не спрятала — опыт не различает" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-16s %-10s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
