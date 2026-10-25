"""Ось 7: разбор. Контроль первым.

Контроль: pick_first напрямую — все вызовы на одном бэкенде (балансировки нет,
и опыт способен это показать). «Прибито» засчитывается, только если на
бэкенды пришло одно соединение: иначе клиент открыл несколько каналов, и
опыт мерил бы не балансировку вызовов.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import BALANCE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "balance.jsonl")


def read(path=FIXTURE):
    return BALANCE.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["path"] == "direct-pick-first" and r["outcome"] != "pinned":
            raise SystemExit("контроль %s: pick_first распределил вызовы" % r["variant"])
        if r["outcome"] == "pinned" and sum(r["backend_conns"].values()) != 1:
            raise SystemExit("%s: прибито, но соединений на бэкендах %s" % (r["variant"], r["backend_conns"]))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-28s %-7s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
