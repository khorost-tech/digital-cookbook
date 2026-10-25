"""Ось 5: разбор. Контроль первым.

Контроль: без поздних заказов выгрузка по времени события совпадает с эталоном.
Поздние заказы действительно поставлены: у варианта event — потери ровно 20.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import LATE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "late.jsonl")


def read(path=FIXTURE):
    return LATE.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if by["control"]["outcome"] != "match":
        raise SystemExit("контроль: без поздних заказов не совпало с эталоном")
    if by["event"]["orders_missing"] != by["event"]["late_orders"]:
        raise SystemExit("поздние заказы не поставлены: потерь у event %d" % by["event"]["orders_missing"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-22s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
