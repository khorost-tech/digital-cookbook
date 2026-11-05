#!/usr/bin/env python3
"""Разбор оси 3: КОНТРОЛЬ ПЕРВЫМ, таблица — только если он сошёлся.

ЗАПУСК (докер не нужен, читается фикстура):
  python3 scripts/analyze-prepared.py

ПОРЯДОК ЗДЕСЬ — ЧАСТЬ МЕТОДА, А НЕ ОФОРМЛЕНИЕ. Контроль оси 3 —
ОТДЕЛЬНАЯ ПРОБА с литеральным запросом: у неё не должно быть ни одного
параметризованного текста, и при этом тексты у неё обязаны быть сняты.
Не сошлось — признак различает не то, что мы думаем, и печатать тексты
значило бы выдать за наблюдение то, что наблюдением не является.

Сам запрет живёт не здесь, а в `scripts/prepared.table`: сценарий —
самое удобное место обойти его, не тронув ни одной проверки.

НИ ОДНОГО СЛОВА О СКОРОСТИ: печатаются тексты запросов и счёт вызовов.
"""

import json
import os
import sys

STAND = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if STAND not in sys.path:
    sys.path.insert(0, STAND)

from scripts import prepared  # noqa: E402
from scripts.console import force_utf8  # noqa: E402


def main():
    force_utf8()
    if not os.path.exists(prepared.FIXTURE):
        print("нет замера оси 3: %s" % prepared.FIXTURE)
        return 2
    rows = prepared.read(prepared.FIXTURE)
    decision = prepared.verdict(rows)
    control = decision["by_variant"][decision["control"]]

    print("ОТРИЦАТЕЛЬНЫЙ КОНТРОЛЬ (проба %s: тот же образ, тот же клиент "
          "базы, литерал вместо параметра)" % decision["control"])
    print("  текстов снято %d; с параметром %d; с величиной %d"
          % (control["texts_sampled"], control["parameterized"],
             control["literal"]))
    print("  параметризованных вызовов нет: %s; тексты вообще снимались: "
          "%s; проба собралась тем же клиентом базы (снято ldd): %s"
          % ("да" if decision["control_shows_only_literal"] else "НЕТ",
             "да" if decision["every_arm_was_seen_at_work"] else "НЕТ",
             "да" if decision["control_uses_the_same_database_client"]
             else "НЕТ"))
    print("  штатные варианты показали только параметр: %s; запросы дошли "
          "до базы у всех: %s; клиент базы один: %s"
          % ("да" if decision["parameterized_arms_show_only_the_parameter"]
             else "НЕТ",
             "да" if decision["every_arm_reached_the_database"] else "НЕТ",
             "да" if decision["one_driver_for_every_arm"] else "НЕТ"))
    if not decision["calls_match_requests_everywhere"]:
        # Оговорка, а не условие печати: расхождение не делает текст
        # неверным, но запрещает называть прирост числом вызовов плеча.
        print("  ЧИСЛО ВЫЗОВОВ РАЗОШЛОСЬ С ЧИСЛОМ ЗАПРОСОВ — прирост "
              "нельзя называть числом вызовов плеча")

    if not decision["prepared_publishable"]:
        print("")
        print("ОСЬ 3 НЕДЕЙСТВИТЕЛЬНА — таблица не печатается.")
        print(json.dumps(decision, ensure_ascii=False, sort_keys=True,
                         indent=2))
        return 1

    print("")
    for line in prepared.table(rows):
        print(line)
    print("")
    if decision["layers_agree_on_the_text"]:
        print("Слой на текст запроса не влияет: у трёх слоёв он один и тот "
              "же. Это ожидалось (ручки и клиент базы общие) и записано как "
              "есть — вытягивать отсюда находку нечего.")
    else:
        print("ТЕКСТ ЗАПРОСА РАЗЛИЧАЕТСЯ ПО СЛОЯМ — это находка, и её надо "
              "объяснять замером, а не рассуждением.")
    print("Про подготовленные запросы стенд не утверждает ничего: снаружи "
          "их не видно (pg_prepared_statements — представление сессии), а "
          "exec_params строк там не создаёт вовсе.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
