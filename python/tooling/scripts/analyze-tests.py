"""Разбор наборов тестов: кто что ловит и сколько это стоит.

ЧЕТЫРЕ ВЕЩИ ПРОВЕРЯЮТСЯ ДО ТОГО, как таблица что-то значит.

1. КОНТРОЛЬ ЗЕЛЁНЫЙ. На исправной реализации обязаны пройти все четыре
   набора. Набор, который падает всегда, «ловит» любой дефект — и ни
   одного.

2. КАЖДЫЙ ДЕФЕКТ КЕМ-ТО ПОЙМАН. Если подсунутую ошибку не заметил никто,
   однозначного вывода нет: внедрение не сработало, мутация оказалась
   эквивалентной, данные не проходят через изменённую ветку или проверкам
   не хватает утверждений. Это повод разобрать случай, а не вывод ни о
   замере, ни о тестах, — и таблица до разбора ничего не значит.

3. КАЖДЫЙ НАБОР ЧТО-ТО ЛОВИТ. Набор, не поймавший ничего, в сравнении
   бесполезен: про него нельзя сказать, слаб он или просто не встретил
   своего дефекта. Именно поэтому в перечень добавлен n-plus-1 —
   единственный, который виден только по числу обращений к базе.

4. КОНТРПРИМЕР ЕСТЬ У КАЖДОЙ ПОИМКИ. Property-based набор ценен не тем,
   что покраснел, а тем, какие данные предъявил. Разбор контрпримера
   однажды уже промолчал: искалась фраза, которой в выводе нет, и на
   каждую поимку возвращался None. Всё остальное при этом выглядело
   исправным — поимки на месте, счётчики на месте.

5. НАСТОЯЩАЯ БАЗА БЫЛА НАСТОЯЩЕЙ. Число стартов контейнера считается в
   conftest и обязано быть больше нуля. Иначе «интеграционный набор»
   мог оказаться прогоном без базы, зелёным по недоразумению.
"""

import collections
import json
import os
import statistics as st
import sys
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE",
    Path(__file__).resolve().parent.parent / "fixtures" / "tests.txt"))

SUITES = ("mocks", "sqlite", "postgres", "hypothesis")
CONTROL = "none"
WHAT = {
    "none": "правильная реализация — контроль",
    "null-filter": "<> вместо IS DISTINCT FROM: строки с NULL выпадают",
    "like-case": "LIKE вместо регистронезависимого поиска",
    "group-by": "несгруппированная колонка в SELECT ... GROUP BY",
    "n-plus-1": "запрос на каждого автора вместо одного",
}
TITLE = {"mocks": "моки", "sqlite": "фейк (SQLite)",
         "postgres": "база (Testcontainers)",
         "hypothesis": "hypothesis + база"}


def rows():
    return [json.loads(line) for line in
            FIXTURE.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def med(xs):
    return st.median(xs) if xs else None


def verdict(matrix, hyp=None):
    """matrix: {(defect, suite): row}; hyp: строки property-based прогонов."""
    problems = []
    defects = sorted({d for d, _ in matrix})
    suites = [s for s in SUITES if any(s == x for _, x in matrix)]

    for suite in suites:
        row = matrix.get((CONTROL, suite))
        if row is None:
            problems.append(f"нет контрольного прогона для набора {suite}")
        elif row["failed"]:
            problems.append(
                f"{suite}: на ИСПРАВНОЙ реализации упало {row['failed']} из "
                f"{row['tests']} — набор красный сам по себе, и его "
                "«пойманные» дефекты ничего не значат")

    for defect in defects:
        if defect == CONTROL:
            continue
        catchers = [s for s in suites if matrix.get((defect, s), {}).get(
            "caught")]
        if not catchers:
            problems.append(
                f"дефект {defect} не поймал НИКТО — разобрать до выводов: "
                "не сработало внедрение, мутация эквивалентна, данные не "
                "проходят через ветку или проверкам не хватает утверждений")

    for suite in suites:
        caught = [d for d in defects
                  if d != CONTROL and matrix.get((d, suite), {}).get("caught")]
        if not caught:
            problems.append(
                f"набор {suite} не поймал ни одного дефекта — про него "
                "нельзя сказать, слаб он или не встретил своего случая")

    pg = [row for (d, s), row in matrix.items()
          if s in ("postgres", "hypothesis")]
    if pg and not all((row.get("container_starts") or 0) > 0 for row in pg):
        problems.append(
            "в части прогонов «настоящей базы» контейнер не стартовал ни "
            "разу — набор мог оказаться зелёным без базы")
    problems += verdict_hypothesis(matrix, hyp or [])
    return problems


def verdict_hypothesis(matrix, hyp):
    """Проверки, относящиеся только к property-based плечу."""
    problems = []
    if not hyp:
        return problems
    by = {(r["defect"], r["shrink"]): r for r in hyp}

    control = by.get((CONTROL, False))
    if control:
        limit = control.get("max_examples")
        if control["examples"] != limit:
            problems.append(
                f"контроль property-based прошёл на {control['examples']} "
                f"примерах вместо {limit} — генератор остановился раньше "
                "предела, и «прошёл» сказано о меньшей работе, чем заявлено")

    for (defect, shrink), row in sorted(by.items()):
        if shrink or not row["caught"]:
            continue
        shrunk = by.get((defect, True))
        if not shrunk or not shrunk.get("counterexample"):
            problems.append(
                f"{defect}: поимка есть, а контрпримера нет — разбор вывода "
                "hypothesis молчит, и предъявить читателю нечего")
        elif shrunk["examples"] < row["examples"]:
            problems.append(
                f"{defect}: прогон со сжатием выполнил меньше примеров "
                f"({shrunk['examples']}), чем прогон без сжатия "
                f"({row['examples']}) — сжатие не могло начаться раньше "
                "поимки, значит прогоны несопоставимы")
    return problems


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    matrix = {(r["defect"], r["suite"]): r
              for r in data if r.get("kind") == "matrix"}
    costs = [r for r in data if r.get("kind") == "cost"]
    if not matrix:
        print("в фикстуре нет матрицы", file=sys.stderr)
        return 2

    hyp = [r for r in data if r.get("kind") == "hypothesis"]
    defects = [d for d in WHAT if any(d == x for x, _ in matrix)]
    problems = verdict(matrix, hyp)

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()
    control = {s: matrix.get((CONTROL, s), {}).get("failed")
               for s in SUITES}
    print(f"1) контроль (исправный код), упало: {control} — везде обязан "
          "быть 0")
    pg_starts = sorted({r.get("container_starts")
                        for (d, s), r in matrix.items() if s == "postgres"})
    print(f"2) стартов контейнера в матричных прогонах базы: {pg_starts}")
    print(f"3) состояний кода: {len(defects)}, наборов: {len(SUITES)}")

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return 2
    print("ВЕРДИКТ: замер годен")

    print()
    print("=" * 78)
    print("КТО ЛОВИТ ПОДСУНУТЫЙ ДЕФЕКТ")
    print("=" * 78)
    print()
    print(f"  {'состояние кода':<14}{'моки':>8}{'фейк':>8}{'база':>8}"
          f"{'hypothesis':>12}   что подсунуто")
    for defect in defects:
        cells = []
        for suite in SUITES:
            row = matrix.get((defect, suite))
            if row is None:
                cells.append("—")
            elif defect == CONTROL:
                cells.append("зел." if not row["failed"] else "КРАСН.")
            else:
                cells.append("поймал" if row["caught"] else "нет")
        print(f"  {defect:<14}{cells[0]:>8}{cells[1]:>8}{cells[2]:>8}"
              f"{cells[3]:>12}   {WHAT[defect]}")
    print()
    print("  «поймал» — упал хотя бы один тест набора. Целое да/нет, без")
    print("  оттенков: набор либо стал красным, либо остался зелёным.")

    print()
    print("  Что стоит за строками:")
    print("  null-filter ловят оба SQL-плеча — SQLite трактует NULL так же,")
    print("    как PostgreSQL, и здесь фейк работает по-настоящему.")
    print("  like-case и group-by видит только настоящая база: у SQLite LIKE")
    print("    регистронезависим для ASCII, а несгруппированную колонку он")
    print("    разрешает. Оба раза фейк зелёный, и оба раза он неправ.")
    print("  n-plus-1 не меняет РЕЗУЛЬТАТ, поэтому оба плеча, сверяющие")
    print("    результат, молчат — включая настоящую базу. Видит его только")
    print("    набор на моках, считающий обращения к соединению.")

    if hyp:
        by_defect = {(r["defect"], r["shrink"]): r for r in hyp}
        print()
        print("=" * 78)
        print("PROPERTY-BASED: СКОЛЬКО ПРИМЕРОВ ДО ОШИБКИ")
        print("=" * 78)
        print()
        print("  Два прогона меряют разное. Без сжатия видно, за сколько")
        print("  примеров генератор наткнулся на ошибку. Со сжатием — сколько")
        print("  ещё раз он прогнал тело, СОКРАЩАЯ пример. Считать примеры на")
        print("  прогоне со сжатием значит мерить работу сжатия, а не поиска.")
        print()
        limit = max((r.get("max_examples") or 0) for r in hyp)
        print(f"  {'состояние кода':<14}{'без сжатия':>12}{'со сжатием':>12}"
              f"{'поймал':>10}")
        for defect in defects:
            plain = by_defect.get((defect, False))
            shrunk = by_defect.get((defect, True))
            if not plain:
                continue
            mark = "да" if plain["caught"] else "нет"
            tail = "" if plain["caught"] else f"   (предел {limit})"
            print(f"  {defect:<14}{plain['examples']:>12}"
                  f"{(shrunk or {}).get('examples', '—'):>12}{mark:>10}{tail}")

        print()
        print("=" * 78)
        print("КОНТРПРИМЕРЫ ПОСЛЕ СОКРАЩЕНИЯ, ДОСЛОВНО")
        print("=" * 78)
        for defect in defects:
            shrunk = by_defect.get((defect, True))
            if not shrunk or not shrunk.get("counterexample"):
                continue
            print()
            print(f"  --- {defect}: {WHAT[defect]}")
            for line in shrunk["counterexample"].splitlines():
                print("  " + line)
        print()
        print("  Сокращённый — не значит «самый маленький из возможных»:")
        print("  hypothesis упрощает пример, пока упрощение сохраняет")
        print("  падение, и останавливается на достигнутом. Для like-case")
        print("  этого хватило до одной строки и запроса из одной буквы.")

    if costs:
        print()
        print("=" * 78)
        print("ЦЕНА ПРОГОНА (исправный код, покругово)")
        print("=" * 78)
        print()
        by = collections.defaultdict(list)
        starts = {}
        for r in costs:
            key = (r["suite"], r["scope"])
            by[key].append(r["seconds"])
            starts[key] = r.get("container_starts")
        print(f"  {'набор':<22}{'область':<12}{'медиана, с':>12}"
              f"{'размах':>16}{'стартов':>10}")
        for key in sorted(by, key=lambda k: (SUITES.index(k[0]), k[1])):
            v = sorted(by[key])
            span = f"{v[0]:.1f}-{v[-1]:.1f}"
            print(f"  {TITLE[key[0]]:<22}{key[1]:<12}{med(v):>12.1f}"
                  f"{span:>16}{str(starts[key]):>10}")
        print()
        print("  Старты СЧИТАЮТСЯ в conftest, а не выводятся из области")
        print("  видимости. «session — значит один раз» звучит очевидно ровно")
        print("  до первой ошибки в приспособлении, а стоит эта ошибка вдвое.")

        fn = sorted(by.get(("postgres", "function"), []))
        se = sorted(by.get(("postgres", "session"), []))
        if fn and se:
            print()
            print(f"  Одни и те же три теста: {med(se):.1f} с при одном "
                  f"контейнере против {med(fn):.1f} с при трёх — "
                  f"в {med(fn) / med(se):.1f} раза дороже за ту же проверку.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
