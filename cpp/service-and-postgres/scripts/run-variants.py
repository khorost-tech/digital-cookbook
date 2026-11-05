#!/usr/bin/env python3
"""Обмер трёх вариантов: собрать образы и записать fixtures/variants.jsonl.

ЗАЧЕМ ЭТОТ ФАЙЛ. Утверждение «различается только слой HTTP» держалось на
прозе: версии инструментов снимались проверкой, сравнивались и
выбрасывались, а в статью и в отчёт шли строчки текста. В волне A ровно
так и было — имя образа в JSON, — и причинную трактовку пришлось отзывать
после написания статей. Здесь замер попадает в фикстуру, фикстура лежит в
репозитории, а проверки читают её, а не отчёт.

ЧТО СНИМАЕТСЯ: версии компилятора, cmake и libpqxx в каждом образе;
версия библиотеки слоя; флаги сборки ОБЩЕЙ библиотеки; переменные
окружения и точка входа образа; поведение при испорченной настройке
(код возврата и открывался ли порт); ПРОЧИТАННАЯ занятость при
заданном числе рабочих единиц вместе с двумя её контролями.

ЗАПУСК (из каталога стенда, где есть докер):
  python3 scripts/run-variants.py

Файл перезаписывается целиком: дописывание к прежнему прогону дало бы
склейку двух прогонов, выглядящую одним.
"""

import io
import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402  (после правки sys.path)
from scripts.console import force_utf8  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "variants.jsonl")


def main():
    force_utf8()
    rows = []
    for name in sorted(dockerlib.variants()):
        print("собираю %s" % name)
        dockerlib.build_variant(name)
        print("меряю %s" % name)
        rows.append(dockerlib.measure_variant(name))
    os.makedirs(os.path.dirname(FIXTURE), exist_ok=True)
    with io.open(FIXTURE, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False,
                                    sort_keys=True) + "\n")
        # Маркер завершения в КОНЦЕ файла: оборванный прогон не должен
        # выглядеть полным.
        handle.write(END_MARKER + "\n")
    for row in rows:
        print("%-7s единиц %d, в полёте разом %s"
              % (row["variant"], row["occupancy"]["workers"],
                 row["occupancy"]["runs"]))
    print("записано: %s" % FIXTURE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
