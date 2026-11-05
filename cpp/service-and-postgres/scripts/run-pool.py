#!/usr/bin/env python3
"""Замер оси 2 «сколько соединений открыто на самом деле» → fixtures/pool.jsonl.

ЗАПУСК (из каталога стенда, где есть докер):
  python3 scripts/run-pool.py

Поднимает PostgreSQL, гоняет по каждому варианту пару настроек — пул на 1
и пул на 8, всё остальное одинаково — и пишет фикстуру целиком.
Дописывания нет: склейка двух прогонов выглядела бы одним.

Сюда же попадает ОТДЕЛЬНАЯ строка про собственный пул Drogon
(`kind: "drogon-own-pool"`). В саму ось она не входит и помечена
`used_in_axis: false`: собственный пул фреймворка ходит в базу через
libpq самого Drogon, а три варианта оси — через libpqxx, и ось
сравнивала бы драйверы, а не слои. Но раз про асимметрию говорится —
она замеряется, а не объявляется.

Само правило вывода живёт в scripts/pool.py и проверяется без докера:
числа сюда попадают, но решение о том, что они значат, принимает
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
from scripts import drogonpool  # noqa: E402
from scripts import pg  # noqa: E402
from scripts import pool  # noqa: E402
from scripts.console import force_utf8  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402

FIXTURE = os.path.join(STAND, "fixtures", "pool.jsonl")


def main():
    force_utf8()
    rows = []
    # База поднимается ОДНА на весь прогон: вариант, замеренный в своей
    # базе, сравнивался бы с соседом при других условиях общего ресурса.
    # Без pg_stat_statements — ось считает соединения, а расширение не
    # бесплатно (см. scripts/pg.start).
    database = pg.start(statements=False)
    try:
        for name in sorted(dockerlib.variants()):
            print("собираю %s" % name)
            dockerlib.build_variant(name)
            for size in pool.SIZES:
                print("меряю %s при пуле %d" % (name, size))
                rows.append(pool.measure(name, pool_size=size))
        for size in pool.SIZES:
            print("меряю собственный пул drogon при %d" % size)
            rows.append(drogonpool.measure(size))
    finally:
        database.stop()
    os.makedirs(os.path.dirname(FIXTURE), exist_ok=True)
    with io.open(FIXTURE, "w", encoding="utf-8", newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False,
                                    sort_keys=True) + "\n")
        # Маркер завершения в КОНЦЕ файла: оборванный прогон не должен
        # выглядеть полным.
        handle.write(END_MARKER + "\n")
    for row in rows:
        print("%-18s %-7s пул %d → соединений %d (после залпа %d), "
              "занятость %s"
              % (row["kind"], row["variant"], row["pool_size"],
                 row["connections_observed"],
                 row.get("connections_idle_after", -1),
                 row.get("inflight_peak")))
    print("записано: %s" % FIXTURE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
