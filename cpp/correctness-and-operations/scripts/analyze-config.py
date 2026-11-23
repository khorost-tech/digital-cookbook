"""Ось 4: что сделал сервис на значении STAND_PORT. Контроль первым.

Контроль: корректное значение стартует у обоих способов чтения. Входы с
пробелом и плюсом обязательны: atoi их принимает, from_chars — нет; без них
ось не различает способы там, где различие есть.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import CONFIG  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "config.jsonl")


def read(path=FIXTURE):
    return CONFIG.read_fixture(path)


def rows_for(rows, **match):
    return [r for r in rows if all(r.get(k) == v for k, v in match.items())]


def outcome(rows, reader, input_name):
    found = rows_for(rows, reader=reader, input_name=input_name)
    if len(found) != 1:
        raise SystemExit("%s/%s: строк %d" % (reader, input_name, len(found)))
    return found[0]["outcome"]


def check_controls(rows):
    for reader in ("naive", "strict"):
        if outcome(rows, reader, "valid") != "served":
            raise SystemExit("контроль: корректное значение не стартовало у %s" % reader)
    names = {r["input_name"] for r in rows}
    if not {"leading-space", "plus-sign"} <= names:
        raise SystemExit("нет входов с пробелом и плюсом")


def main():
    rows = read()
    check_controls(rows)
    for name in [r["input_name"] for r in rows_for(rows, reader="naive")]:
        n = rows_for(rows, reader="naive", input_name=name)[0]
        s = rows_for(rows, reader="strict", input_name=name)[0]
        print("%-14s %-8r naive=%-17s порт %-6s strict=%-9s %s" % (
            name, n["value"], n["outcome"], n["effective_port"], s["outcome"], s["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
