"""Ось 4: разбор. Контроль первым.

Контроль: полный backfill — 14 запусков, все успешны, витрина = эталон. Счёт
запусков сходится: успешно + сбой + не выполнено = запусков. Отказ CLI —
только у вариантов, где он и проверяется.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import BACKFILL  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "backfill.jsonl")
REFUSED = ("ordered-default", "ordered-clean", "backwards-ordered")


def read(path=FIXTURE):
    return BACKFILL.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["success"] + r["failed"] + r["not_run"] != r["runs"]:
            raise SystemExit("%s: счёт запусков не сходится" % r["variant"])
        if (r["cli_rc"] != 0) != (r["variant"] in REFUSED):
            raise SystemExit("%s: отказ CLI не там, где ожидался" % r["variant"])
    f = by["full"]
    if (f["runs"], f["success"], f["matched_days"]) != (f["days"], f["days"], f["days"]):
        raise SystemExit("контроль: полный backfill не дал эталон")


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-18s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
