"""Ось 4: разбор. Контроль первым.

Контроль: обработчик в обоих порядках получает ровно вызовы с токеном —
авторизация работает. Различие порядков — в том, что видят метрики и журнал.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import CHAIN  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "chain.jsonl")
N = 10


def read(path=FIXTURE):
    return CHAIN.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["kind"] == "panic-metrics":
            continue
        if r["handler_calls"] != N:
            raise SystemExit("контроль %s: до обработчика дошло %d" % (r["variant"], r["handler_calls"]))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-28s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
