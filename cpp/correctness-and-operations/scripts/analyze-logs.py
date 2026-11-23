"""Ось 6: остаются ли JSON-логи JSON. Контроль первым.

Для каждой пары «логгер × сообщение» снято: сколько строк лога дала одна
запись (lines), сколько из них разобрал json.loads (parsed), корректный ли это
UTF-8 (valid_utf8) и совпало ли разобранное сообщение с исходным (round_trip).

Байт 0xFF читается Python'ом через surrogateescape, и это названо: так
json.loads принимает строку, которую строгий разборщик UTF-8 отверг бы, —
поэтому valid_utf8 снимается отдельно.

Контроль: обычное сообщение — одна строка, разобрана, совпала, у обоих.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_measurements  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "logs.jsonl")

# Те же сообщения, что в logs/log_probe.cpp, байт в байт.
MESSAGES = {
    "plain": "пользователь вошёл".encode(),
    "quote": "имя \"admin\" не найдено".encode(),
    "backslash": "путь C:\\temp\\x".encode(),
    "newline": "строка один\nстрока два".encode(),
    "byte-ff": "байт ".encode() + b"\xff" + " из запроса".encode(),
}


def judge(output, message):
    """output — байты вывода одной записи. Возвращает величины строки."""
    lines = [l for l in output.split(b"\n") if l]
    parsed, round_trip = 0, False
    for line in lines:
        try:
            obj = json.loads(line.decode("utf-8", "surrogateescape"))
        except ValueError:
            continue
        parsed += 1
        got = obj.get("msg", "").encode("utf-8", "surrogateescape")
        round_trip = round_trip or (len(lines) == 1 and got == MESSAGES[message])
    try:
        output.decode("utf-8")
        valid = True
    except UnicodeDecodeError:
        valid = False
    return {"lines": len(lines), "parsed": parsed, "valid_utf8": valid, "round_trip": round_trip}


def read(path=FIXTURE):
    return read_measurements(path)


def cell(rows, logger, message):
    found = [r for r in rows if r["logger"] == logger and r["message"] == message]
    if len(found) != 1:
        raise SystemExit("%s/%s: строк %d" % (logger, message, len(found)))
    return found[0]


def check_controls(rows):
    for logger in ("pattern", "escaped"):
        c = cell(rows, logger, "plain")
        if not (c["lines"] == 1 and c["parsed"] == 1 and c["round_trip"]):
            raise SystemExit("контроль: обычное сообщение у %s — %r" % (logger, c))


def main():
    rows = read()
    check_controls(rows)
    for r in rows:
        print("%-8s %-10s lines=%d parsed=%d utf8=%s round_trip=%s" % (
            r["logger"], r["message"], r["lines"], r["parsed"], r["valid_utf8"], r["round_trip"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
