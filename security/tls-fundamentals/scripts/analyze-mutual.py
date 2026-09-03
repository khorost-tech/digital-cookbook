"""Разбор оси взаимного рукопожатия.

МЕРА — СЧЁТНАЯ, А НЕ ОЦЕНОЧНАЯ.

«Диагностика клиента менее конкретна, чем серверная» — суждение, которое
нечем проверить. Считается вместо него другое: сколько РАЗЛИЧИМЫХ причин
видно с каждой стороны. Если два разных слома дают одинаковое сообщение,
по этому сообщению их не различить, и сторона видит одну причину вместо
двух. Это можно посчитать и перепроверить.

Отдельно сверяются причины: совпадает ли то, что назвал клиент, с тем,
что записал сервер. Расхождение здесь — не ошибка замера, а находка:
клиент может честно назвать симптом, о котором его известили, и при этом
ничего не сказать о настоящей причине.
"""

import io
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_fixture

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

CONTROL = "mutual_valid"


def normalize(text):
    """Сводит сообщение к его сути, чтобы считать различимость.

    Убираются номера, адреса и имена классов — они у каждой стороны свои
    и различают не причины, а реализации. Остаётся то, ЧТО сказано.
    """
    text = text.lower()
    text = re.sub(r"[0-9a-f]{6,}", "", text)
    text = re.sub(r"\d+", "", text)
    text = re.sub(r"[^a-zа-я_ ]+", " ", text)
    return " ".join(text.split())


def main():
    rows = read_fixture(os.path.join(HERE, "fixtures/mutual.jsonl"))
    clients = sorted({r["client"] for r in rows})
    cases = []
    for r in rows:
        if r["case"] not in cases:
            cases.append(r["case"])
    broken = [c for c in cases if c != CONTROL]

    by = {(r["case"], r["client"]): r for r in rows}

    print("ВЗАИМНОЕ РУКОПОЖАТИЕ")
    print()
    control_bad = [c for c in clients
                   if by[(CONTROL, c)]["outcome"] != "connected"]
    if control_bad:
        raise SystemExit("контроль не прошёл у %r — остальные строки "
                         "недействительны" % control_bad)
    print("Контроль пройден всеми четырьмя: сломы ниже сравнивать можно.")
    print()

    width = max(len(c) for c in cases) + 2
    print("случай".ljust(width) + "".join(c.ljust(12) for c in clients))
    for case in cases:
        line = case.ljust(width)
        for client in clients:
            row = by[(case, client)]
            mark = row["outcome"]
            if row.get("handshake_ok") is True and mark == "rejected":
                mark = "отказ после"
            elif mark == "rejected":
                mark = "отказ"
            elif mark == "connected":
                mark = "соединился"
            line += mark.ljust(12)
        print(line)

    print()
    print("СКОЛЬКО РАЗЛИЧИМЫХ ПРИЧИН ВИДНО С КАЖДОЙ СТОРОНЫ")
    print("  (%d сломанных случая; если два дают одно сообщение —" % len(broken))
    print("   по сообщению их не различить)")
    print()
    server_side = {normalize(by[(c, clients[0])].get("server_saw", ""))
                   for c in broken}
    print("  сервер:  %d из %d" % (len(server_side), len(broken)))
    for client in clients:
        seen = {normalize(by[(c, client)]["detail"]) for c in broken}
        print("  %-8s %d из %d" % (client + ":", len(seen), len(broken)))

    print()
    print("ГДЕ КЛИЕНТ И СЕРВЕР ГОВОРЯТ О РАЗНОМ")
    print("  (клиент называет то, о чём его известили; сервер — то, что")
    print("   увидел. Это не всегда одно и то же событие.)")
    for case in broken:
        saw = by[(case, clients[0])].get("server_saw", "")
        for client in clients:
            detail = by[(case, client)]["detail"]
            print("  %-16s %-8s сервер: %s" % (case, client, saw[:66]))
            print("  %-16s %-8s клиент: %s" % ("", "", detail[:66]))
        print()


if __name__ == "__main__":
    main()
