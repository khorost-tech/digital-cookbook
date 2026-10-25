"""Ось 2: разбор. Контроль первым.

Контроль: один прогон без падения во всех режимах — эталон; иначе дубли и
потери в остальных сценариях — свойство постановки, а не режима загрузки.
Падение обязано случиться: в сценарии crash-reload второй запуск — failed.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import IDEMPOTENCY  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "idempotency.jsonl")


def read(path=FIXTURE):
    return IDEMPOTENCY.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["scenario"] == "once" and r["outcome"] != "match":
            raise SystemExit("контроль %s: один прогон не дал эталон" % r["variant"])
        if r["scenario"] == "crash-reload" and r["runs"][1] != "failed":
            raise SystemExit("%s: падение не случилось" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-28s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
