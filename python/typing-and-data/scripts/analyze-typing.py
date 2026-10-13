"""Разбор матрицы типов: кто что ловит и чего не ловит никто.

ЧТО ПРОВЕРЯЕТСЯ ДО ТОГО, как таблица что-то значит.

1. КОНТРОЛЬ ЗЕЛЁНЫЙ. На исправном коде обязаны молчать все пять плеч.
   Плечо, красное само по себе, «ловит» любой дефект и ни одного.

2. КАЖДЫЙ ДЕФЕКТ КЕМ-ТО ПОЙМАН. Если подсунутую ошибку не заметил никто,
   это сообщение о замере, а не вывод о проверяющих: код с ней ведёт себя
   так же, как без неё.

3. КАЖДОЕ ПЛЕЧО ЧТО-ТО ЛОВИТ. Плечо без единой поимки в сравнении
   бесполезно: про него нельзя сказать, слабое оно или просто не встретило
   своего случая.

4. ВСЕ ПЛЕЧИ ВИДЕЛИ ОДИН ФАЙЛ. Хеш исходников до первого плеча и после
   последнего обязан совпасть, иначе кто-то переписал код под собой.

5. НАБЛЮДЕНИЕ СОВПАЛО С ЗАМЫСЛОМ. Стенд объявляет в defects.EXPECTED, кто
   какой дефект обязан поймать. Расхождение не «почти то же самое»: либо
   ожидание неверно, либо инструмент изменился, и решать это должен
   человек, а не молчаливое усреднение. Один раз замер уже поправил
   ожидание — см. комментарий в defects.py.
"""

import importlib.util
import json
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURE = Path(os.environ.get("FIXTURE", ROOT / "fixtures" / "typing.txt"))

SPEC = importlib.util.spec_from_file_location("defects", ROOT / "app"
                                              / "defects.py")
defects = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(defects)

ARMS = ("mypy", "mypy-strict", "pyright", "pydantic", "runtime")
TITLE = {
    "mypy": "mypy",
    "mypy-strict": "mypy --strict",
    "pyright": "pyright",
    "pydantic": "pydantic",
    "runtime": "выполнение",
}


def rows():
    return [json.loads(line) for line in
            FIXTURE.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def verdict(data):
    problems = []
    by_defect = {r["defect"]: r for r in data}

    missing = [d for d in defects.DEFECTS if d not in by_defect]
    if missing:
        problems.append(
            f"в фикстуре нет состояний: {', '.join(missing)} — матрица "
            "неполная, и по ней нельзя сказать, чего плечо не ловит")
        return problems

    control = by_defect[defects.NONE]
    red = [a for a in ARMS if control["arms"][a]["caught"]]
    if red:
        problems.append(
            f"на ИСПРАВНОМ коде красные плечи: {', '.join(red)} — их "
            "«пойманные» дефекты ничего не значат")

    for defect in defects.DEFECTS:
        if defect == defects.NONE:
            continue
        row = by_defect[defect]
        catchers = tuple(a for a in ARMS if row["arms"][a]["caught"])
        if not catchers:
            problems.append(
                f"дефект {defect} не поймал НИКТО — значит он не дефект: "
                "код с ним ведёт себя так же, как без него")
            continue
        expected = tuple(defects.EXPECTED.get(defect, ()))
        if catchers != expected:
            problems.append(
                f"{defect}: поймали {', '.join(catchers) or 'никто'}, а "
                f"ожидалось {', '.join(expected) or 'никто'} — либо "
                "ожидание неверно, либо инструмент изменился")

    for arm in ARMS:
        caught = [d for d in defects.DEFECTS
                  if d != defects.NONE and by_defect[d]["arms"][arm]["caught"]]
        if not caught:
            problems.append(
                f"плечо {arm} не поймало ни одного дефекта — про него нельзя "
                "сказать, слабое оно или не встретило своего случая")

    for row in data:
        if row["sha256_before"] != row["sha256_after"]:
            problems.append(
                f"{row['defect']}: исходники изменились за прогон "
                f"({row['sha256_before']} -> {row['sha256_after']}) — какой-то "
                "проверяющий переписал код под собой, и плечи смотрели на "
                "разное")

    for defect in defects.DEFECTS:
        row = by_defect[defect]
        for arm in ARMS:
            cell = row["arms"][arm]
            if cell["caught"] and not cell["evidence"]:
                problems.append(
                    f"{defect}/{arm}: поимка есть, а выдержки из вывода нет — "
                    "предъявить читателю нечего")
    return problems


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    if not data:
        print("фикстура пуста", file=sys.stderr)
        return 2
    by_defect = {r["defect"]: r for r in data}
    problems = verdict(data)

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()
    control = by_defect.get(defects.NONE, {}).get("arms", {})
    print("1) контроль (исправный код): "
          + ", ".join(f"{a}={'КРАСНОЕ' if control.get(a, {}).get('caught') else 'зел.'}"
                      for a in ARMS))
    hashes = {r["defect"]: r["sha256_before"] for r in data}
    print(f"2) состояний кода: {len(data)}, у каждого свой хеш исходников: "
          f"{len(set(hashes.values()))} различных")
    print("3) исходники за прогон не менялись: "
          + ("да" if all(r["sha256_before"] == r["sha256_after"]
                         for r in data) else "НЕТ"))

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for item in problems:
            print("  - " + item)
        return 2
    print("ВЕРДИКТ: замер годен")

    print()
    print("=" * 78)
    print("КТО ЛОВИТ ПОДСУНУТЫЙ ДЕФЕКТ")
    print("=" * 78)
    print()
    header = f"  {'состояние кода':<20}"
    for arm in ARMS:
        header += f"{TITLE[arm]:>14}"
    print(header)
    for defect in defects.DEFECTS:
        row = by_defect[defect]
        line = f"  {defect:<20}"
        for arm in ARMS:
            cell = row["arms"][arm]
            if defect == defects.NONE:
                word = "КРАСНОЕ" if cell["caught"] else "зел."
            else:
                word = "поймал" if cell["caught"] else "нет"
            line += f"{word:>14}"
        print(line + f"   {defects.WHAT[defect]}")
    print()
    print("  «поймал» — код возврата не ноль. Целое да/нет, без оттенков.")
    print("  Читать таблицу стоит как карту слепых зон, а не как турнирную")
    print("  таблицу: ни одно плечо не покрывает всё, и ни одно не лишнее.")

    print()
    print("=" * 78)
    print("ЧТО ИМЕННО УВИДЕЛ КАЖДЫЙ — ВЫДЕРЖКИ ИЗ ВЫВОДА")
    print("=" * 78)
    for defect in defects.DEFECTS:
        if defect == defects.NONE:
            continue
        row = by_defect[defect]
        print()
        print(f"  --- {defect}: {defects.WHAT[defect]}")
        for arm in ARMS:
            cell = row["arms"][arm]
            if cell["caught"]:
                print(f"    {TITLE[arm]:<14} {cell['evidence']}")
        silent = [TITLE[a] for a in ARMS if not row["arms"][a]["caught"]]
        if silent:
            print(f"    молчат:        {', '.join(silent)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
