"""Разбор разложения по слоям.

Что проверяется до того, как печатать числа:

1. Плечи отдавали одинаковый результат ВО ВРЕМЯ замера, а не только на
   проверке равенства до него. Хеш лежит в каждой строке фикстуры.
2. Разброс по кругам внутри клетки. Выше порога — клетка непригодна для
   ранжирования, и это помечается, а не усредняется.
3. Строки без полей, которые добавила текущая версия измерителя: такая
   фикстура снята другой версией, и разбор пошёл бы не по этому прогону.

Доля от сырого драйвера считается ПОКРУГОВО: плечо и его пол меряются в
одном круге, поэтому общее состояние машины частично сокращается. Отношение
медиан дало бы другое число и было бы хуже.
"""

import collections
import json
import os
import statistics as st
import sys
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE",
    Path(__file__).resolve().parent.parent / "fixtures" / "layers.txt"))

FLOOR = "raw-asyncpg"
ARM_ORDER = ("raw-asyncpg", "core-asyncpg", "orm-asyncpg",
             "orm-psycopg-async", "orm-psycopg-sync")
QUERIES = ("one-row", "many-rows", "heavy-sql", "heavy-rows",
           "heavy-entities")
SPREAD_LIMIT = 1.5
# Запросы, где почти всё время принадлежит серверу. Помечаются в выводе:
# мерить цену слоя на них нельзя, они показывают, где разница исчезает.
SERVER_BOUND = ("heavy-sql", "heavy-rows", "heavy-entities")
# У heavy-sql есть вторая оговорка, и она важнее первой: ORM-плечи
# выполняют там выражение Core и отдают mappings, потому что агрегат не
# ложится на сущность. Материализации объектов в этом столбце нет вовсе, и
# вывод про её цену из него не следует.
NO_ENTITIES = ("heavy-sql", "heavy-rows")
# Единственная пара, где различается РОВНО ОДНО: тот же SQL, та же работа
# сервера, разница только в том, строит ли ORM объекты.
CONTROLLED_PAIR = ("heavy-rows", "heavy-entities")


def rows():
    return [json.loads(line) for line in
            FIXTURE.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def med(xs):
    return st.median(xs) if xs else None


def paired_delta(data, arm, q_hi, q_lo):
    """Покруговые разности времени между двумя запросами одного плеча.

    Разность МЕДИАН и медиана РАЗНОСТЕЙ — не одно и то же. Первая берёт два
    независимых ряда и вычитает их сводки, теряя связь между кругами;
    вторая сравнивает то, что снято в одном круге, где состояние машины
    общее. Для пары, на которой держится вывод, нужна вторая.
    """
    by_round = collections.defaultdict(dict)
    for r in data:
        if r["arm"] == arm and r["query"] in (q_hi, q_lo):
            by_round[r["round"]][r["query"]] = r["mean_ms"]
    out = []
    for pair in by_round.values():
        if q_hi in pair and q_lo in pair:
            out.append(pair[q_hi] - pair[q_lo])
    return sorted(out)


def paired(data, arm, query):
    """Покруговые отношения времени плеча ко времени сырого драйвера."""
    by_round = collections.defaultdict(dict)
    for r in data:
        if r["query"] == query and r["arm"] in (arm, FLOOR):
            by_round[r["round"]][r["arm"]] = r["mean_ms"]
    out = []
    for pair in by_round.values():
        if arm in pair and FLOOR in pair and pair[FLOOR]:
            out.append(pair[arm] / pair[FLOOR])
    return sorted(out)


def main():
    if not FIXTURE.exists():
        print(f"нет фикстуры {FIXTURE}", file=sys.stderr)
        return 2
    data = rows()
    if not data:
        print("фикстура пуста", file=sys.stderr)
        return 2

    cells = collections.defaultdict(list)
    for r in data:
        cells[(r["arm"], r["query"])].append(r)

    problems = []
    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()

    stale = [r for r in data if "sha256" not in r or "iterations" not in r]
    print(f"1) строк без полей текущего измерителя: {len(stale)} из {len(data)}")
    if stale:
        problems.append(f"{len(stale)} строк снято другой версией измерителя — "
                        "разбор идёт не по этому прогону")

    print("2) совпадение результата внутри запроса (хеш из каждой строки):")
    for q in QUERIES:
        hs = {r["sha256"] for r in data if r["query"] == q}
        print(f"   {q:<10} различных хешей: {len(hs)}")
        if len(hs) > 1:
            problems.append(f"{q}: плечи отдавали разное ВО ВРЕМЯ замера "
                            f"({len(hs)} хешей) — равенство работы нарушено")

    wide = []
    for (arm, q), rs in sorted(cells.items()):
        v = sorted(x["mean_ms"] for x in rs)
        if v and v[0] > 0 and v[-1] / v[0] > SPREAD_LIMIT:
            wide.append((arm, q, v[-1] / v[0]))
    print(f"3) клеток с разбросом выше {SPREAD_LIMIT}x: {len(wide)} из {len(cells)}")
    for arm, q, x in wide:
        print(f"   {arm}/{q}: {x:.2f}x")

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return 2
    print("ВЕРДИКТ: замер годен")
    if wide:
        print("  (клетки выше для ранжирования непригодны — помечены ниже)")

    print()
    print("=" * 78)
    print("ВРЕМЯ ОДНОЙ ОПЕРАЦИИ, мс (медиана по кругам)")
    print("=" * 78)
    print()
    header = f"  {'плечо':<20}"
    for q in QUERIES:
        header += f"{q:>16}"
    print(header)
    for arm in ARM_ORDER:
        line = f"  {arm:<20}"
        for q in QUERIES:
            v = med([x["mean_ms"] for x in cells.get((arm, q), [])])
            line += f"{v:>16.4f}" if v is not None else f"{'—':>16}"
        print(line)

    print()
    print("=" * 78)
    print("ДОЛЯ ОТ СЫРОГО ДРАЙВЕРА: во сколько раз дороже (медиана и размах)")
    print("=" * 78)
    print()
    print("  Считается покругово: плечо и пол в одном круге, состояние машины")
    print("  частично сокращается. Больше единицы — слой стоит дороже.")
    print()
    header = f"  {'плечо':<20}"
    for q in QUERIES:
        header += f"{q:>22}"
    print(header)
    for arm in ARM_ORDER:
        if arm == FLOOR:
            continue
        line = f"  {arm:<20}"
        for q in QUERIES:
            sh = paired(data, arm, q)
            if sh:
                cell = f"{med(sh):.2f}x ({sh[0]:.2f}-{sh[-1]:.2f})"
                line += f"{cell:>22}"
            else:
                line += f"{'—':>22}"
        print(line)
    print()
    for q in SERVER_BOUND:
        print(f"  ⚠️  {q}: почти всё время принадлежит серверу (см. "
              f"scripts/check-db-share.sh).")
        print("      Числа этого столбца показывают, ГДЕ цена слоя перестаёт")
        print("      быть заметной, но не годятся как мера самой цены.")
    for q in NO_ENTITIES:
        print(f"  ⚠️  {q}: у ORM-плеч здесь НЕТ сущностей — агрегат не ложится")
        print("      на модель, и они выполняют выражение Core, отдавая")
        print("      mappings. Совпадение с Core означает, что тонут")
        print("      накладные расходы сессии, а НЕ что исчезает цена")
        print("      материализации: материализовать тут нечего. Про неё")
        print("      отвечает heavy-entities.")

    print()
    print("=" * 78)
    print("ЦЕНА МАТЕРИАЛИЗАЦИИ ПРИ ОДИНАКОВОМ SQL")
    print("=" * 78)
    print()
    left, right = CONTROLLED_PAIR
    print(f"  {left} и {right} — дословно один и тот же запрос. Сервер")
    print("  выполняет одну и ту же работу; различается только то, строит ли")
    print("  ORM объекты. Это единственная пара в стенде, где меняется ровно")
    print("  одно, поэтому вывод про цену материализации следует из неё, а не")
    print("  из сравнения разных запросов.")
    print()
    print("  Разность считается ПОКРУГОВО и уже из разностей берётся медиана:")
    print("  оба запроса одного круга сняты при общем состоянии машины.")
    print("  Разность медиан двух рядов такой связи не сохраняет.")
    print()
    print("  Главный столбец здесь — ЗНАК. Полные размахи портит один-два")
    print("  выброса, и по ним вывод не построить; знак разности к выбросам")
    print("  нечувствителен. Если эффекта нет, знак должен делиться примерно")
    print("  пополам — так ведут себя контрольные плечи.")
    print()
    print(f"  {'плечо':<20}{'знак +':>9}{'медиана':>11}"
          f"{'межквартильный размах':>26}{'полный размах':>24}")
    for arm in ARM_ORDER:
        d = paired_delta(data, arm, right, left)
        if not d:
            continue
        n = len(d)
        pos = sum(1 for v in d if v > 0)
        q1, q3 = d[n // 4], d[(3 * n) // 4]
        iqr = f"{q1:+.3f} .. {q3:+.3f}"
        span = f"{d[0]:+.3f} .. {d[-1]:+.3f}"
        print(f"  {arm:<20}{f'{pos}/{n}':>9}{med(d):>+11.3f}{iqr:>26}"
              f"{span:>24}")
    print()
    print("  У сырого драйвера и Core разницы быть не должно: для них эти два")
    print("  запроса неразличимы по построению, и знак у них обязан гулять.")
    print("  У плеча с настоящим эффектом знак постоянен во всех кругах.")
    print("  Складывать плечи в одну вероятность нельзя: они меряются на")
    print("  одной машине, делят кэши и попадают в одни и те же провалы —")
    print("  независимыми свидетельствами они не являются. Каждое читается")
    print("  само по себе.")

    print()
    print("=" * 78)
    print("ЦЕНА ОДНОЙ СТРОКИ: сколько стоит материализация, мкс")
    print("=" * 78)
    print()
    print("  Разность времени на 5000 строках и на одной, делённая на 4999.")
    print("  Так из цены уходят постоянные издержки запроса — путь до базы,")
    print("  разбор ответа, — и остаётся то, что растёт с числом строк.")
    print()
    print(f"  {'плечо':<20}{'мкс на строку':>16}{'во сколько раз':>18}")
    base = None
    for arm in ARM_ORDER:
        one = med([x["mean_ms"] for x in cells.get((arm, "one-row"), [])])
        many = med([x["mean_ms"] for x in cells.get((arm, "many-rows"), [])])
        rows_per_op = next((x["rows_per_op"] for x in
                            cells.get((arm, "many-rows"), [])), None)
        if one is None or many is None or not rows_per_op:
            continue
        per_row = (many - one) * 1000 / (rows_per_op - 1)
        if base is None:
            base = per_row
        print(f"  {arm:<20}{per_row:>16.3f}{per_row / base:>17.2f}x")
    return 0


if __name__ == "__main__":
    sys.exit(main())
