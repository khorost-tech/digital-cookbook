"""Разбор матрицы цепочки: прогон против ожиданий, записанных до прогона.

Разбор НЕ содержит ожиданий. Они лежат в expected.json, и это принципиально:
иначе подгонка прогноза под результат выглядела бы как обычная правка кода
и не оставляла следа. Здесь только сверка и печать.

Печатается три вещи:
  1. сама матрица — исход в каждой клетке;
  2. клетки, где прогон разошёлся с прогнозом, — их и надо объяснять;
  3. чем клиенты различаются при ОДИНАКОВОМ исходе, то есть диагностикой.

Третье — не украшение. Ожидание, записанное заранее, состояло в том, что
матрица окажется скучной: почти везде все четверо ответят одинаково.
Значит содержание не в том, ПУСКАЮТ ли они, а в том, что говорят.
"""

import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import read_fixture

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

MARK = {"connected": "соединился", "rejected": "отказал", "error": "СБОЙ ПРОБЫ"}


def load_json(name):
    with io.open(os.path.join(HERE, name), encoding="utf-8") as handle:
        return json.loads(handle.read())


def expected_for(expected, case, client):
    cell = expected["cases"][case]
    return cell.get("by_client", {}).get(client, cell["outcome"])


def main():
    expected = load_json("expected.json")
    cases = [c["case"] for c in load_json("pki/cases.json")]
    clients = expected["clients"]

    rows = read_fixture(os.path.join(HERE, "fixtures/chain.jsonl"))
    got = {(r["case"], r["client"]): r for r in rows if r["kind"] == "chain"}

    missing = [(c, cl) for c in cases for cl in clients if (c, cl) not in got]
    if missing:
        raise SystemExit("в фикстуре нет клеток: %r" % missing[:5])

    width = max(len(c) for c in cases) + 2
    print("МАТРИЦА ЦЕПОЧКИ ДОВЕРИЯ")
    print()
    print("случай".ljust(width) + "".join(c.ljust(12) for c in clients))
    for case in cases:
        line = case.ljust(width)
        for client in clients:
            line += MARK[got[(case, client)]["outcome"]].ljust(12)
        print(line)

    print()
    surprises = []
    for case in cases:
        for client in clients:
            want = expected_for(expected, case, client)
            have = got[(case, client)]["outcome"]
            if want != have:
                surprises.append((case, client, want, have))

    print("РАСХОЖДЕНИЯ С ПРОГНОЗОМ: %d из %d клеток"
          % (len(surprises), len(cases) * len(clients)))
    for case, client, want, have in surprises:
        print("  %s / %s: ждали %s, получили %s" % (case, client, want, have))
        print("     основание прогноза: %s"
              % expected["cases"][case]["why"])
    if not surprises:
        print("  Прогон совпал с прогнозом во всех клетках. Само по себе это")
        print("  не достоинство: прогноз мог быть просто осторожным.")

    print()
    print("ЕДИНОГЛАСНЫЕ СТРОКИ И РАСХОЖДЕНИЯ")
    split = 0
    for case in cases:
        outcomes = {got[(case, cl)]["outcome"] for cl in clients}
        if len(outcomes) > 1:
            split += 1
            print("  %s: клиенты разошлись — %s" % (case, sorted(outcomes)))
    print("  строк, где все четверо согласны: %d из %d"
          % (len(cases) - split, len(cases)))

    print()
    print("ЧЕМ РАЗЛИЧАЕТСЯ ДИАГНОСТИКА ПРИ ОДИНАКОВОМ ИСХОДЕ")
    print("  (исход один, а сказанное — разное; это и есть цена выбора клиента)")
    for case in cases:
        outcomes = {got[(case, cl)]["outcome"] for cl in clients}
        if outcomes != {"rejected"}:
            continue
        print("  %s:" % case)
        for client in clients:
            detail = got[(case, client)]["detail"]
            print("     %-8s %s" % (client, detail[:110]))


if __name__ == "__main__":
    main()
