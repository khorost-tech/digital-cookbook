#!/usr/bin/env python3
"""Разбор оси 2: КОНТРОЛЬ ПЕРВЫМ, таблица — только если он сошёлся.

ЗАПУСК (докер не нужен, читается фикстура):
  python3 scripts/analyze-pool.py

ПОРЯДОК ЗДЕСЬ — ЧАСТЬ МЕТОДА, А НЕ ОФОРМЛЕНИЕ. Контроль оси 2 — пара по
размеру пула: пул на 1 против пула на 8, всё остальное одинаково.
Счётчик обязан пойти за настройкой. Не пошёл — мы меряем не пул, и
печатать числа значило бы выдать за наблюдение то, что наблюдением не
является. Поэтому сначала печатается контроль, и при несошедшемся
контроле сценарий уходит с ненулевым кодом, НЕ напечатав таблицы.

Сам запрет живёт не здесь, а в `scripts/pool.table`: сценарий — самое
удобное место обойти его, не тронув ни одной проверки, достаточно
напечатать числа самому. В модуле его можно позвать на придуманных
строках и потребовать отказа.

НИ ОДНОГО СЛОВА О СКОРОСТИ. Стенд не меряет ни пропускную способность, ни
задержку, и ось 2 не исключение: печатаются соединения, занятость и
настройки, при которых они сняты.
"""

import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import pool  # noqa: E402
from scripts.console import force_utf8  # noqa: E402


def main():
    force_utf8()
    if not os.path.exists(pool.FIXTURE):
        print("нет замера оси 2: %s" % pool.FIXTURE)
        return 2
    rows = pool.read(pool.FIXTURE)
    decision = pool.verdict(rows)

    print("КОНТРОЛЬ (пул %d против пула %d, всё остальное одинаково)"
          % (min(pool.SIZES), max(pool.SIZES)))
    for name in decision["variants"]:
        item = decision["by_variant"][name]
        print("  %-7s соединений %d → %d; счётчик пошёл за настройкой: %s; "
              "малый пул удержал потолок: %s"
              % (name, item["low"], item["high"],
                 "да" if item["knob_moves_counter"] else "НЕТ",
                 "да" if item["low_within_pool"] else "НЕТ"))
    print("  ничего не открыто до залпа: %s; пул не течёт: %s; "
          "все запросы обслужены: %s; клиент базы один на три слоя: %s"
          % (decision["nothing_open_before_the_volley"], decision["no_leak"],
             decision["all_served"], decision["one_driver_for_every_layer"]))
    if decision["rows_with_impossible_numbers"]:
        print("  СОЕДИНЕНИЙ БОЛЬШЕ, ЧЕМ ЗАПРОСОВ БЫВАЛО В ПОЛЁТЕ: %s — "
              "столько соединений взять было некому"
              % ", ".join(decision["rows_with_impossible_numbers"]))
    if not decision["every_number_stable_within_the_session"]:
        # Не условие печати, а оговорка: неустойчивое число не делает
        # замер неверным, но запрещает называть его величиной.
        print("  ЧИСЛО УСТОЙЧИВО НЕ ВЕЗДЕ — строки с плавающим числом нельзя "
              "называть величиной, только наблюдением этого прогона")

    if not decision["pool_publishable"]:
        print("")
        print("ОСЬ 2 НЕДЕЙСТВИТЕЛЬНА — таблица не печатается.")
        print(json.dumps(decision, ensure_ascii=False, sort_keys=True,
                         indent=2))
        return 1

    print("")
    for line in pool.table(rows):
        print(line)

    own = pool.read(pool.FIXTURE, kind="drogon-own-pool")
    if own:
        print("")
        print("СОБСТВЕННЫЙ ПУЛ DROGON — замерен отдельно и в ось НЕ входит: "
              "он ходит в базу клиентом самого фреймворка, а три варианта "
              "оси — общим для них libpqxx; в одной таблице сравнивались бы "
              "драйверы, а не слои.")
        for row in sorted(own, key=lambda item: item["pool_size"]):
            print("  пул %-3d одновременных %-3d соединений %d (клиент %s; "
                  "слинкована с libpq: %s, с libpqxx: %s — снято с "
                  "собранного бинарника, а не объявлено)"
                  % (row["pool_size"], row["concurrent"],
                     row["connections_observed"], row["driver"],
                     row["links_libpq"], row["links_libpqxx"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
