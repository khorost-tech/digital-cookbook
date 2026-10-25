"""Ось 1: разбор. Контроль первым.

Контроль: при сроке больше работы исполнитель доделывает все шаги и клиент
получает OK — иначе цепочка сломана сама по себе. «Узнал об отмене» без
«бросил работу» не засчитывается: исход stopped требует меньше шагов, чем
задано. С новым контекстом клиент всё равно получает DEADLINE_EXCEEDED — срок
у клиента свой, — но исполнитель работу доделывает: это и есть потеря.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import DEADLINE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "deadline.jsonl")


def read(path=FIXTURE):
    return DEADLINE.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["deadline_ms"] >= 1000 and r["outcome"] != "completed":
            raise SystemExit("контроль %s: %s" % (r["variant"], r["outcome"]))
        if r["outcome"] == "stopped" and r["b_steps_done"] >= 10:
            raise SystemExit("%s: «бросил», но доделал всё" % r["variant"])
        if r["deadline_ms"] < 1000 and r["client_code"] != "DeadlineExceeded":
            raise SystemExit("%s: клиент не получил DEADLINE_EXCEEDED" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-34s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
