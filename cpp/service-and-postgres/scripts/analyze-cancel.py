#!/usr/bin/env python3
"""Разбор оси 4: КОНТРОЛЬ ПЕРВЫМ, таблица — только если он сошёлся.

ЗАПУСК (докер не нужен, читается фикстура):
  python3 scripts/analyze-cancel.py

ПОРЯДОК ЗДЕСЬ — ЧАСТЬ МЕТОДА, А НЕ ОФОРМЛЕНИЕ. Контроль оси 4 — то же
плечо при нормальном завершении: клиент дождался ответа, и активных
запросов варианта после ответа обязан быть ноль. Не сошлось — счёт видит
чужие запросы, и ненулевое число у брошенного плеча не значит ничего.

Сам запрет живёт не здесь, а в `scripts/cancel.report`: сценарий — самое
удобное место обойти его, не тронув ни одной проверки. Там же живёт и
подпись объяснения: причину без пометки «ОБЪЯСНЕНИЕ (не замер)»
напечатать нельзя.

НИ ОДНОГО СЛОВА О СКОРОСТИ: печатается счёт активных запросов.
"""

import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import cancel  # noqa: E402
from scripts.console import force_utf8  # noqa: E402


def main():
    force_utf8()
    if not os.path.exists(cancel.FIXTURE):
        print("нет замера оси 4: %s" % cancel.FIXTURE)
        return 2
    rows = cancel.read(cancel.FIXTURE)
    decision = cancel.verdict(rows)

    print("КОНТРОЛЬ (клиент дождался ответа: активных после ответа ноль)")
    for name in decision["variants"]:
        item = decision["by_variant"][name]
        print("  %-8s ответ получен: %s; активных после ответа %d"
              % (name, "да" if item["replied"] else "НЕТ",
                 item["active_after_reply"]))
    print("  контроль сошёлся у всех: %s; запрос был виден активным до "
          "обрыва: %s; клиент ушёл до ответа: %s; счёт снят по имени "
          "варианта: %s"
          % ("да" if decision["control_reads_zero"] else "НЕТ",
             "да" if decision["query_was_seen_active"] else "НЕТ",
             "да" if decision["client_left_before_the_reply"] else "НЕТ",
             "да" if decision["every_count_is_by_the_variant_name"]
             else "НЕТ"))
    if not decision["the_query_ends_on_its_own"]:
        # Оговорка, а не условие печати: сессия, не вернувшаяся к нулю, —
        # находка, и называть её «переживанием обрыва» нельзя.
        print("  ЗАПРОС НЕ КОНЧИЛСЯ И ПОСЛЕ ВСЕГО ОКНА — речь о застрявшей "
              "сессии, а не о переживании обрыва")

    if not decision["cancel_publishable"]:
        print("")
        print("ОСЬ 4 НЕДЕЙСТВИТЕЛЬНА — таблица не печатается.")
        print(json.dumps(decision, ensure_ascii=False, sort_keys=True,
                         indent=2))
        return 1

    print("")
    for line in cancel.report(rows):
        print(line)
    return 0


if __name__ == "__main__":
    sys.exit(main())
