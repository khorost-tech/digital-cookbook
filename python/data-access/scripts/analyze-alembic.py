"""Разбор сценария Alembic.

Ответы здесь целые — «да/нет», — и потому переживут любой перезамер, в
отличие от процентов. Но именно поэтому важно не сложить в одну колонку
три разных вопроса:

    увидела ли автогенерация изменение
    удалось ли применить то, что она предложила
    вернул ли откат схему в исходное состояние

Между первым и вторым лежит целый класс случаев: изменение замечено, а
предложенная миграция на непустой таблице не исполняется. «Обнаружено» —
не то же самое, что «сработает».
"""

import collections
import json
import os
import sys
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE",
    Path(__file__).resolve().parent.parent / "fixtures" / "alembic.txt"))

CONTROL = "контроль"


def rows(path=None):
    p = Path(path) if path else FIXTURE
    return [json.loads(line) for line in
            p.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def verdict(data):
    """Возвращает (код, замечания). Чистая функция — тестируется."""
    problems = []
    controls = [r for r in data if r["case"].startswith(CONTROL)]
    if not controls:
        problems.append("контрольного случая нет — без него лишние операции "
                        "от расхождения описания с базой неотличимы от "
                        "найденных изменений")
    for r in controls:
        if r["detected"]:
            problems.append(
                f"контроль в режиме «{r['mode']}» обнаружил {r['operations']} "
                "на неизменённом описании — описание расходится с базой, "
                "остальные случаи не читаются")

    # Случай, который ничего не менял и потому «прошёл», выглядит как успех.
    for r in data:
        if r["case"].startswith(CONTROL):
            continue
        if not r["detected"] and r["applied"]:
            problems.append(f"{r['case']}/{r['mode']}: изменение не найдено, "
                            "но что-то применилось — так не бывает")
        if r["applied"] and not r["upgrade_changed_schema"]:
            problems.append(f"{r['case']}/{r['mode']}: миграция применилась, "
                            "но схема не изменилась — проверялось не то")
    return (2 if problems else 0), problems


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    code, problems = verdict(data)

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return code
    print("ВЕРДИКТ: годен — контроль чист, применённые миграции меняли схему")

    modes = []
    for r in data:
        if r["mode"] not in modes:
            modes.append(r["mode"])
    by = {(r["case"], r["mode"]): r for r in data}
    cases = []
    for r in data:
        if r["case"] not in cases:
            cases.append(r["case"])

    print()
    print("=" * 78)
    print("ЧТО ВИДИТ АВТОГЕНЕРАЦИЯ И ЧТО ВОЗВРАЩАЕТ ОТКАТ")
    print("=" * 78)
    print()
    print(f"  Alembic {data[0]['alembic']}")
    print()
    head = f"  {'изменение схемы':<34}"
    for m in modes:
        head += f"{m:>22}"
    print(head)
    for case in cases:
        line = f"  {case:<34}"
        for m in modes:
            r = by.get((case, m))
            if r is None:
                line += f"{'—':>22}"
            elif case.startswith(CONTROL):
                line += f"{'операций нет' if not r['detected'] else 'ЕСТЬ':>22}"
            elif not r["detected"]:
                line += f"{'не видит':>22}"
            elif r["applied"] is False:
                line += f"{'видит, не смог':>22}"
            elif r["restored"]:
                line += f"{'видит, откат вернул':>22}"
            else:
                line += f"{'видит, откат НЕ вернул':>22}"
        print(line)

    print()
    print("  ЧИТАЕТСЯ ТАК. «Не видит» — изменение прошло бы мимо миграции")
    print("  молча. «Видит, не смог» — миграция сгенерирована, но на непустой")
    print("  таблице не исполняется: обнаружение и работоспособность — разные")
    print("  вещи. «Откат вернул» проверено сравнением снимков системного")
    print("  каталога, а не текстом миграции.")

    misses = [(r["case"], r["mode"]) for r in data
              if not r["case"].startswith(CONTROL) and not r["detected"]]
    fails = [(r["case"], r["mode"], r["error"]) for r in data
             if r["applied"] is False]
    if misses:
        print()
        print("  ПРОШЛО БЫ МИМО МИГРАЦИИ:")
        for case, mode in misses:
            print(f"    {case} (режим: {mode})")
    if fails:
        print()
        print("  СГЕНЕРИРОВАНО, НО НЕ ИСПОЛНЯЕТСЯ:")
        seen = set()
        for case, mode, err in fails:
            if case in seen:
                continue
            seen.add(case)
            print(f"    {case}: {err}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
