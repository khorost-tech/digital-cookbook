#!/usr/bin/env python3
"""Замер оси 3 «параметр против литерала» → fixtures/prepared.jsonl.

ЗАПУСК (из каталога стенда, где есть докер):
  python3 scripts/run-prepared.py

Поднимает PostgreSQL СО СТАТИСТИКОЙ (`pg_stat_statements` требует флага
при старте сервера — это замерено, см. scripts/pg.start), гоняет три
штатных варианта и КОНТРОЛЬНУЮ ПРОБУ с литеральным текстом и пишет
фикстуру целиком.
Дописывания нет: склейка двух прогонов выглядела бы одним.

УЧАСТНИКИ МЕРЯЮТСЯ ПО ОЧЕРЕДИ, И ЭТО УСЛОВИЕ ЗАМЕРА, А НЕ ПОРЯДОК РАДИ
ПОРЯДКА. Текст живого запроса читается по `application_name`, то есть
разделён по участникам; число вызовов из `pg_stat_statements` не
разделено никак — у представления нет ни `pid`, ни имени приложения.
Прирост привязан к участнику только тем, что в базу в это время не ходит
никто другой.

Само правило вывода живёт в scripts/prepared.py и проверяется без
докера: числа сюда попадают, но решение о том, что они значат, принимает
проверяемый код, а не этот сценарий.
"""

import io
import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402
from scripts import prepared  # noqa: E402
from scripts.console import force_utf8  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402


def main():
    force_utf8()
    rows = []
    # База поднимается ОДНА на весь прогон: плечо, замеренное в своей
    # базе, сравнивалось бы с соседом при других условиях общего ресурса.
    database = pg.start(statements=True)
    try:
        for name in sorted(dockerlib.variants()):
            print("собираю %s" % name)
            dockerlib.build_variant(name)
            print("меряю %s" % name)
            rows.append(prepared.measure(name, database))
        # Контрольная проба ПОСЛЕДНЕЙ: отдельный бинарник, собираемый
        # внутри образа варианта тем же компилятором и тем же libpqxx.
        # Сервиса она не касается — почему именно так, сказано в
        # scripts/prepared.py.
        print("готовлю образ для контрольной пробы")
        prepared.build_control_arm()
        print("меряю %s" % prepared.CONTROL)
        rows.append(prepared.measure_control(database))
    finally:
        database.stop()
    os.makedirs(os.path.dirname(prepared.FIXTURE), exist_ok=True)
    with io.open(prepared.FIXTURE, "w", encoding="utf-8",
                 newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False,
                                    sort_keys=True) + "\n")
        # Маркер завершения в КОНЦЕ файла: оборванный прогон не должен
        # выглядеть полным.
        handle.write(END_MARKER + "\n")
    for row in rows:
        print("%-14s %-8s текстов %d (с параметром %d, с величиной %d), "
              "вызовов в базе %d при %d запросах"
              % (row["variant"], row["sql_mode"], row["texts_sampled"],
                 row["parameterized_texts"], row["literal_texts"],
                 row["calls_delta"], row["requests"]))
    print("записано: %s" % prepared.FIXTURE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
