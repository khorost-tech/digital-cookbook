"""Ось 3: разбор. Контроль первым.

Контроль: задача по data_interval_start обрабатывает каждый из 14 дней ровно
раз. Счёт дней сходится: ровно раз + больше раза + ни разу = 14.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import INTERVAL  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "interval.jsonl")


def read(path=FIXTURE):
    return INTERVAL.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["variant"].startswith(("scheduled-", "manual-")):
            continue
        if r["once"] + r["many"] + r["never"] != r["days"]:
            raise SystemExit("%s: счёт дней не сходится" % r["variant"])
    if by["interval"]["once"] != by["interval"]["days"]:
        raise SystemExit("контроль: задача по интервалу обработала не каждый день ровно раз")


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-9s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
