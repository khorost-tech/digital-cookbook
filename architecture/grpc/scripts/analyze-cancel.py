"""Ось 2: разбор. Контроль первым.

Контроль: без отмены клиент получает все сообщения и сервер отмены не видит.
Отмена поставлена, только если клиент прочитал ровно заданное и сервер отмену
заметил. Сервер без проверки отмены обязан продолжить цикл — иначе «проверка
останавливает» ничего не доказывает.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import CANCEL  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "cancel.jsonl")


def read(path=FIXTURE):
    return CANCEL.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["mode"] == "control" and r["stream_cancel_seen"]:
            raise SystemExit("контроль %s: сервер увидел отмену без отмены" % r["variant"])
        if r["mode"] not in ("check", "control") and r["outcome"] != "kept-sending":
            raise SystemExit("%s: без проверки цикл остановился сам — опыт не различает" % r["variant"])
        if r["mode"].endswith("-handler") and not r["cancel_handler_calls"]:
            raise SystemExit("%s: обработчик отмены не вызван — его время ничего не показывает" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-22s %-13s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
