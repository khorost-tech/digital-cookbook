"""Ось 5: разбор. Контроль первым.

Контроль: быстрый клиент рядом с медленным получил все события — иначе ломается
опыт, а не политика. Событий заведомо больше, чем вмещают буферы сокетов: иначе
очередь сервера «не растёт» из-за ядра. Напрямую каждая политика обязана
сработать (иначе сравнивать с прокси не с чем).
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import SLOW  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "slow.jsonl")
SOCKET_BUFFERS = 1 << 20   # с большим запасом больше суммы буферов отправки и приёма
DIRECT = {"unbounded": "grew", "drop-oldest": "dropped", "disconnect": "disconnected"}


def read(path=FIXTURE):
    return SLOW.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if not r["fast_all"] or r["fast_missing"]:
            raise SystemExit("контроль %s: быстрый клиент получил не всё" % r["variant"])
        if r["n"] * r["payload"] < 2 * SOCKET_BUFFERS:
            raise SystemExit("%s: событий меньше, чем вмещают буферы сокетов" % r["variant"])
        if r["path"] == "direct" and r["outcome"] != DIRECT[r["policy"]]:
            raise SystemExit("контроль %s: напрямую политика не сработала (%s)" % (r["variant"], r["outcome"]))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-36s %-13s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
