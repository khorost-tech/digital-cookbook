"""Ось 1: разбор цены события. Контроль первым.

Контроль: все события доставлены без дублей у каждого варианта, и у вариантов
без сжатия байт на событие не меньше полезной нагрузки — иначе счёт байт
сломан. Цена события пересчитывается из сырых прогонов разностью.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import WIRE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "wire.jsonl")


def read(path=FIXTURE):
    return WIRE.read_fixture(path)


def per_event(row, field):
    a, b = (row["runs"][k] for k in sorted(row["runs"], key=int))
    k = int(max(row["runs"], key=int)) - int(min(row["runs"], key=int))
    return round((b[field] - a[field]) / k, 1)


def check(rows):
    for r in rows:
        if r["outcome"] != "delivered":
            raise SystemExit("%s: %s — %s" % (r["variant"], r["outcome"], r["detail"]))
        if per_event(r, "bytes_down") != r["down_per_event"]:
            raise SystemExit("%s: байт на событие не пересчитывается из прогонов" % r["variant"])
        if not r["compress"] and r["down_per_event"] < r["payload"]:
            raise SystemExit("%s: %s байт на событие меньше нагрузки %d — счёт сломан"
                             % (r["variant"], r["down_per_event"], r["payload"]))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-22s %7.1f ↓ %6.1f ↑ %5.2f запр. %5.2f пустых" % (
            r["variant"], r["down_per_event"], r["up_per_event"], r["requests_per_event"], r["empty_per_event"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
