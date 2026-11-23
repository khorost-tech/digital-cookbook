"""Ось 5: дескрипторы на пути ошибки. Контроль первым.

Счёт дескрипторов — из /proc/<pid>/fd ВНУТРИ контейнера (поле source),
а не из ответа сервиса: сервис — предмет замера, и его слово о себе
проверкой не служит. Число запросов с ошибкой сверяется по ответам: 500
должно прийти ровно M раз, иначе прирост делился бы не на те запросы.
Контроль: M запросов без ошибки — прирост 0 у обоих вариантов.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_measurements  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "fds.jsonl")


def read(path=FIXTURE):
    return read_measurements(path)


def row(rows, ownership, fail):
    found = [r for r in rows if r["ownership"] == ownership and r["fail"] == fail]
    if len(found) != 1:
        raise SystemExit("%s/fail=%d: строк %d" % (ownership, fail, len(found)))
    return found[0]


def check(rows):
    for r in rows:
        if r["source"] != "/proc/1/fd":
            raise SystemExit("%s: счёт не из /proc" % r["variant"])
        expected = {500: r["m"]} if r["fail"] else {200: r["m"]}
        got = {int(k): v for k, v in r["responses"].items()}
        if got != expected:
            raise SystemExit("%s/fail=%d: ответы %r, ожидалось %r" % (r["ownership"], r["fail"], got, expected))
    for own in ("raw", "raii"):
        if row(rows, own, 0)["delta"] != 0:
            raise SystemExit("контроль: %s теряет дескрипторы и без ошибки" % own)


def main():
    rows = read()
    check(rows)
    for r in rows:
        print(json.dumps({k: r[k] for k in ("ownership", "fail", "m", "fd_before", "fd_after", "delta")}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
