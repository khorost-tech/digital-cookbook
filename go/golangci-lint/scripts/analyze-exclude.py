"""Ось 4: разбор. Контроль первым.

Контроль: конфиг без исключений находит всё размеченное, и ничего вне
разметки. Без этого «пропало от исключения» нельзя отличить от «не нашлось вовсе».
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import EXCLUDE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "exclude.jsonl")


def read(path=FIXTURE):
    return EXCLUDE.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    base = by["base"]
    if base["hidden_want"] or base["hidden_noise"] or base["extra"]:
        raise SystemExit("контроль: без исключений найдено не всё размеченное")
    for r in rows:
        if r["found"] + len(r["hidden_want"]) != base["found"]:
            raise SystemExit("%s: найдено + спрятано не равно контролю" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-30s %-15s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
