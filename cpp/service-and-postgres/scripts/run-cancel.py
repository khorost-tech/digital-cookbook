#!/usr/bin/env python3
"""Замер оси 4 «переживает ли запрос уход клиента» → fixtures/cancel.jsonl.

ЗАПУСК (из каталога стенда, где есть докер):
  python3 scripts/run-cancel.py

Поднимает PostgreSQL БЕЗ статистики (`pg_stat_statements` здесь не
спрашивается ни разу, а флаг при старте сервера — это другая база), гоняет
три варианта и пишет фикстуру целиком. Дописывания нет: склейка двух
прогонов выглядела бы одним.

У КАЖДОГО ВАРИАНТА ДВЕ СТРОКИ, И ПОРЯДОК ИХ ФИКСИРОВАН: сперва контроль
(клиент дождался ответа), потом уход клиента. Обратный порядок считал бы
остаток брошенного запроса контролем — см. `scripts/cancel.measure`.

Сервис при этом НЕ ТРОГАЕТСЯ ВОВСЕ: уход клиента делает проба своим
сокетом. Почему так — в докстринге `scripts/cancel.py`.

Само правило вывода живёт в scripts/cancel.py и проверяется без докера:
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

from scripts import cancel  # noqa: E402
from scripts import docker as dockerlib  # noqa: E402
from scripts import pg  # noqa: E402
from scripts.console import force_utf8  # noqa: E402
from scripts.outcome import END_MARKER  # noqa: E402


def main():
    force_utf8()
    rows = []
    # База поднимается ОДНА на весь прогон: вариант, замеренный в своей
    # базе, сравнивался бы с соседом при других условиях общего ресурса.
    database = pg.start(statements=False)
    try:
        for name in sorted(dockerlib.variants()):
            print("собираю %s" % name)
            dockerlib.build_variant(name)
            print("меряю %s" % name)
            rows.extend(cancel.measure(name, database))
    finally:
        database.stop()
    os.makedirs(os.path.dirname(cancel.FIXTURE), exist_ok=True)
    with io.open(cancel.FIXTURE, "w", encoding="utf-8",
                 newline="\n") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False,
                                    sort_keys=True) + "\n")
        # Маркер завершения в КОНЦЕ файла: оборванный прогон не должен
        # выглядеть полным.
        handle.write(END_MARKER + "\n")
    for row in rows:
        if row["arm"] == cancel.AWAITED:
            print("%-8s контроль: ответ %s, активных после ответа %d"
                  % (row["variant"], row["status"],
                     row["active_after_reply"]))
        else:
            print("%-8s уход клиента: активных до обрыва %d, байт до обрыва "
                  "%d, активных после обрыва %d, через всё окно %d"
                  % (row["variant"], row["active_before_close"],
                     row["bytes_before_close"], row["active_after_close"],
                     row["active_after_window"]))
    print("записано: %s" % cancel.FIXTURE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
