"""Разбор сценария N+1.

Главная величина — ЧИСЛО ЗАПРОСОВ, и она целая. Поэтому и проверки здесь
жёстче, чем во временных сценариях: число запросов обязано быть одинаковым
во всех кругах. Если оно гуляет, значит в счёт попало что-то постороннее, и
разбирать такую фикстуру нельзя.

Вторая обязательная проверка — что стратегии загрузили ОДНО И ТО ЖЕ.
Стратегия, которая обошлась одним запросом, потому что ничего не загрузила,
выглядит в таблице лучше всех.
"""

import collections
import json
import os
import sys
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE",
    Path(__file__).resolve().parent.parent / "fixtures" / "nplus1.txt"))

ORDER = ("lazy", "selectinload", "joinedload")
# Стратегия, объявленная как «без N+1», не может делать запрос на объект.
# Порог с запасом: у selectinload их три, у joinedload один.
NO_NPLUS1 = ("selectinload", "joinedload")
NO_NPLUS1_LIMIT = 10


def rows(path=None):
    p = Path(path) if path else FIXTURE
    return [json.loads(line) for line in
            p.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def verdict(data):
    """Возвращает (код, список замечаний). Чистая функция — тестируется."""
    problems = []
    by = collections.defaultdict(list)
    for r in data:
        by[r["strategy"]].append(r)

    # 1. Стратегии обязаны загрузить одно и то же.
    loaded = {s: {(r["books"], r["reviews"], r["rating_sum"]) for r in rs}
              for s, rs in by.items()}
    distinct = {next(iter(v)) for v in loaded.values() if len(v) == 1}
    if any(len(v) != 1 for v in loaded.values()):
        problems.append("внутри стратегии загружено разное между кругами — "
                        "набор данных менялся во время прогона")
    elif len(distinct) > 1:
        problems.append(f"стратегии загрузили РАЗНОЕ ({len(distinct)} набора) "
                        "— сравнивать их нельзя: стратегия, которая ничего не "
                        "загрузила, выглядит лучшей")

    # 2. Число запросов обязано быть целым и постоянным.
    for s, rs in by.items():
        q = {r["queries"] for r in rs}
        if len(q) != 1:
            problems.append(f"{s}: число запросов гуляет по кругам {sorted(q)} "
                            "— в счёт попало постороннее")

    # 3. Стратегия без N+1 не должна делать запрос на объект.
    for s in NO_NPLUS1:
        if s not in by:
            continue
        q = max(r["queries"] for r in by[s])
        if q > NO_NPLUS1_LIMIT:
            problems.append(f"{s}: {q} запросов — это N+1, а не устранение "
                            f"N+1 (порог {NO_NPLUS1_LIMIT})")

    return (2 if problems else 0), problems


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    if not data:
        print("фикстура пуста", file=sys.stderr)
        return 2

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
    print("ВЕРДИКТ: годен — стратегии загрузили одно и то же, число запросов")
    print("         постоянно во всех кругах")

    by = collections.defaultdict(list)
    for r in data:
        by[r["strategy"]].append(r)
    sample = data[0]

    print()
    print("=" * 78)
    print(f"ЗАГРУЗКА {sample['authors']} АВТОРОВ СО ВСЕМИ КНИГАМИ И ОТЗЫВАМИ")
    print("=" * 78)
    print()
    print(f"  загружено одинаково: {sample['books']} книг, "
          f"{sample['reviews']} отзывов")
    print()
    print(f"  {'стратегия':<16}{'запросов':>10}{'строк с сервера':>18}"
          f"{'мс (медиана)':>16}")
    for s in ORDER:
        rs = by.get(s)
        if not rs:
            continue
        ms = sorted(r["ms"] for r in rs)[len(rs) // 2]
        print(f"  {s:<16}{rs[0]['queries']:>10}"
              f"{rs[0]['rows_from_server']:>18}{ms:>16.1f}")

    print()
    lazy = by.get("lazy", [{}])[0]
    if lazy:
        print(f"  У ленивой загрузки {lazy['queries']} запроса складываются "
              f"так: один за авторами,")
        print(f"  {sample['authors']} за их книгами и {sample['books']} за "
              f"отзывами к каждой книге.")
        print("  Это не N+1, а N+1+M: уровней вложенности два, и цена растёт")
        print("  не суммой, а по каждому уровню отдельно.")

    print()
    print("  ЧТО ЗДЕСЬ ИЗМЕРЕНО, А ЧТО НЕТ. Измерены запросы и строки,")
    print("  отданные сервером, — обе величины целые и воспроизводимые.")
    print("  Байты по проводу стенд НЕ измеряет, поэтому цена joinedload")
    print("  видна здесь не полностью: он отдаёт меньше строк, но в каждой")
    print("  повторяет столбцы автора и книги столько раз, сколько у них")
    print("  отзывов. Меньшее число строк у него — не признак меньшего")
    print("  трафика.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
