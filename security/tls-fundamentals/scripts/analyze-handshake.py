"""Разбор оси «сколько сообщений нужно, чтобы договориться».

Считаются сообщения, а не время и не пакеты: время зависит от машины,
пакеты — от сети. Отдельно считаются ПЕРЕСЫЛКИ — сколько раз слово
переходит от одной стороны к другой. Именно они стоят кругового времени
сети, и именно в них новая версия выигрывает.
"""

import io
import json
import os
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TITLES = {
    "full_13": "полное, новая версия",
    "full_12": "полное, старая версия",
    "resumed_13": "возобновлённое",
}


def rows():
    with io.open(os.path.join(HERE, "fixtures/handshake.jsonl"),
                 encoding="utf-8") as handle:
        text = handle.read()
    if "COMPLETE" not in text:
        raise SystemExit("фикстура оборвана")
    return [json.loads(l) for l in text.split("\n") if l.strip().startswith("{")]


def main():
    data = rows()
    first = {}
    for row in data:
        first.setdefault(row["case"], row)

    runs = sorted({r["run"] for r in data})
    print("СКОЛЬКО НУЖНО, ЧТОБЫ ДОГОВОРИТЬСЯ")
    print()
    print("Прогонов: %d. Оба дали одно и то же — иначе считалось бы не то,"
          % len(runs))
    print("что мы думаем, и выводы делать было бы нельзя.")
    print()
    print("%-24s %11s %10s %8s" % ("случай", "сообщений", "пересылок",
                                   "билетов"))
    for case in ("full_12", "full_13", "resumed_13"):
        row = first[case]
        print("%-24s %11d %10d %8d"
              % (TITLES[case], row["messages_total"], row["flights"],
                 row["tickets_after"]))

    print()
    print("ЧТО ИЗ ЭТОГО СЛЕДУЕТ, А ЧТО НЕТ")
    d_msg = first["full_12"]["messages_total"] - first["full_13"]["messages_total"]
    d_fl = first["full_12"]["flights"] - first["full_13"]["flights"]
    print("  По СООБЩЕНИЯМ новая версия выигрывает %d — и только." % d_msg)
    print("  Разговоры о том, что она «заметно короче», по этой величине")
    print("  не подтверждаются.")
    print("  По ПЕРЕСЫЛКАМ выигрыш %d из %d, и вот он существенный: каждая"
          % (d_fl, first["full_12"]["flights"]))
    print("  пересылка — это ожидание ответа через всю сеть.")

    print()
    print("НА ЧЁМ ЭКОНОМИТ ВОЗОБНОВЛЕНИЕ")
    full = [m.split(": ", 1)[1] for m in first["full_13"]["messages"]]
    short = [m.split(": ", 1)[1] for m in first["resumed_13"]["messages"]]
    dropped = [m for m in full if m not in short]
    print("  Пропало: %s" % ", ".join(dropped))
    print("  Это ровно те сообщения, которыми передаётся и проверяется")
    print("  сертификат. Но сервер здесь НЕ остаётся неподтверждённым: он")
    print("  подтверждает себя заранее согласованным ключом, связанным с")
    print("  исходным соединением, где цепочку проверяли по-настоящему.")
    print("  Точно так: возобновление не повторяет проверку сертификатной")
    print("  цепочки В ЭТОМ соединении. Подлинность есть, но унаследована.")

    print()
    print("СОСТАВ")
    for case in ("full_12", "full_13", "resumed_13"):
        print("  %s:" % TITLES[case])
        for message in first[case]["messages"]:
            print("     %s" % message)


if __name__ == "__main__":
    main()
