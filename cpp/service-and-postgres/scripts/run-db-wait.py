#!/usr/bin/env python3
"""Замер «занимает ли ожидание в базе рабочую единицу» → fixtures/db-wait.jsonl.

ЗАПУСК (из каталога стенда, где есть докер):
  python3 scripts/run-db-wait.py

Поднимает PostgreSQL, гоняет пробы по трём вариантам при одной и при двух
рабочих единицах и пишет фикстуру целиком. Дописывания нет: склейка двух
прогонов выглядела бы одним.

Само правило вывода живёт в scripts/dbwait.py и проверяется без докера —
числа сюда попадают, но решение о том, что они значат, принимается
проверяемым кодом, а не этим сценарием.
"""

import io
import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import dbwait  # noqa: E402
from scripts import docker as dockerlib  # noqa: E402
from scripts.console import force_utf8  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "db-wait.jsonl")


def main():
    force_utf8()
    rows = []
    dbwait.start_postgres()
    try:
        for name in sorted(dockerlib.variants()):
            # ОБРАЗ СОБИРАЕТСЯ ЗАНОВО, а не берётся по тегу. Тег —
            # подвижный указатель внутри демона: под ним лежит то, что
            # собралось последним, хоть бы и из чужого дерева. Замер
            # тогда меряет чужой бинарник, и сверка сумм спасает только
            # если суммы СЛУЧАЙНО разошлись — обратный случай (тег несёт
            # верную сумму от чужой сборки) она не ловит вовсе. Найдено
            # ревью: копия, где правился только python-файл, покраснела
            # на сверке суммы, потому что тег нёс образ предыдущей копии.
            print("собираю %s" % name)
            dockerlib.build_variant(name)
            print("меряю %s" % name)
            rows.append(dbwait.measure(name))
    finally:
        dbwait.stop_postgres()
    os.makedirs(os.path.dirname(FIXTURE), exist_ok=True)
    with io.open(FIXTURE, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False,
                                    sort_keys=True) + "\n")
        handle.write(END_MARKER + "\n")
    for row in rows:
        print("%-7s одна единица %s, контроль (%d) %s, перекрылся "
              "всегда: %s"
              % (row["variant"], row["at"][0]["runs"],
                 row["at"][1]["workers"], row["at"][1]["runs"],
                 row["control_overlaps_always"]))
    print("записано: %s" % FIXTURE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
