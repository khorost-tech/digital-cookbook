"""Разбор сценария установки.

Что проверяется до того, как печатать числа:

1. Все инструменты установили ОДИН набор — хеш из каждой строки фикстуры.
   Проверка равенства стоит и до замера, но «до» не гарантирует «во время».
2. Холодный прогон был холодным — по размерам кэшей, записанным ДО него.
   Это главная ловушка волны: «холодная установка» с недочищенным кэшем
   даёт правдоподобные числа и молчит.
3. Разброс по кругам — но только там, где он что-то значит. См. ниже.

ПОРОГ РАЗБРОСА ПРИМЕНЯЕТСЯ НЕ КО ВСЕМ КЛЕТКАМ. На величинах порядка сотых
долей секунды отношение макс/мин говорит о разрешении таймера, а не о
разбросе: 0,01 против 0,03 — это 3x, и никакой нестабильности за этим нет.
Поэтому порог применяется к клеткам, чья медиана выше пола; остальные
помечаются как «слишком быстро, чтобы судить по отношению».
"""

import collections
import itertools
import json
import math
import os
import statistics as st
import sys
from pathlib import Path

FIXTURE = Path(os.environ.get(
    "FIXTURE",
    Path(__file__).resolve().parent.parent / "fixtures" / "install.txt"))

TOOLS = ("uv", "poetry", "pip")
STATES = ("cold", "warm", "noop")
BASE = "uv"
SPREAD_LIMIT = 1.5
# Порог знакового теста: порядок считается установленным, если счёт по
# кругам отличим от подбрасывания монеты.
SIGN_ALPHA = 0.05
# Ниже этого медианного времени отношение макс/мин не о разбросе.
SPREAD_FLOOR_S = 0.2
# Столько кэша допустимо перед холодным прогоном: инструменты создают
# файлы блокировок сразу при запуске.
COLD_LIMIT_MIB = 0.5
# Признак «фикстура снята другой версией сценария»: на такой фикстуре
# остальные проверки не применимы, потому что полей, по которым они
# судят, просто нет.
STALE = "фикстура снята другой версией сценария"


def rows():
    """Только основные измерения — те, что сняты с локального индекса.

    Строки плеча против PyPI помечены kind и сюда НЕ попадают. Смешать их
    с основными значило бы подмешать в медианы величины, снятые в других
    условиях и объявленные невоспроизводимыми.
    """
    return [r for r in all_rows() if "kind" not in r]


def all_rows():
    return [json.loads(line) for line in
            FIXTURE.read_text(encoding="utf-8").splitlines()
            if line.strip() and not line.startswith("#")]


def pypi_rows():
    return [r for r in all_rows() if r.get("kind") == "pypi"]


def med(xs):
    return st.median(xs) if xs else None


def paired(data, tool, state, base=BASE):
    """Покруговые отношения инструмента к базовому в том же состоянии."""
    by_round = collections.defaultdict(dict)
    for r in data:
        if r["state"] == state and r["tool"] in (tool, base):
            by_round[r["round"]][r["tool"]] = r["seconds"]
    out = []
    for pair in by_round.values():
        if tool in pair and base in pair and pair[base]:
            out.append(pair[tool] / pair[base])
    return sorted(out)


def cache_gain(data, tool, order=None):
    """Покруговая разность cold минус warm: что даёт тёплый кэш.

    order=None — все круги; "cold-first" — только те, где холодный замер
    шёл раньше тёплого; "warm-first" — где раньше шёл тёплый.

    РАЗДЕЛЕНИЕ ПО ПОРЯДКУ И ЕСТЬ ОТВЕТ НА ГЛАВНЫЙ ВОПРОС К ЭТОМУ ЗАМЕРУ.
    Пока состояния шли строго cold, warm, noop, разность включала не
    только эффект кэша, но и эффект позиции: чем позже замер, тем теплее
    страничный кэш и процессор. Отличить одно от другого в тех данных
    было нечем.

    Теперь порядок ротируется, и вопрос решается счётом: если выигрыш —
    эффект позиции, то в кругах, где тёплый шёл ПЕРВЫМ, он обязан
    уменьшиться или сменить знак. Если он там такой же, позиция ни при чём.
    """
    by_round = collections.defaultdict(dict)
    for r in data:
        if r["tool"] == tool and r["state"] in ("cold", "warm"):
            by_round[r["round"]][r["state"]] = r
    out = []
    for pair in by_round.values():
        if "cold" not in pair or "warm" not in pair:
            continue
        cold_first = (pair["cold"]["state_position"]
                      < pair["warm"]["state_position"])
        if order == "cold-first" and not cold_first:
            continue
        if order == "warm-first" and cold_first:
            continue
        out.append(pair["cold"]["seconds"] - pair["warm"]["seconds"])
    return sorted(out)


def position_counts(data, factor="state_position"):
    """Сколько раз состояние (или инструмент) стояло на каждой позиции."""
    key = "state" if factor == "state_position" else "tool"
    counts = collections.defaultdict(collections.Counter)
    for r in data:
        counts[r[key]][r[factor]] += 1
    return counts


FACTORS = ("tool_position", "state_position")


def by_position(data, tool, state, factor="state_position"):
    """Времена клетки, разложенные по одному из факторов порядка."""
    out = collections.defaultdict(list)
    for r in data:
        if r["tool"] == tool and r["state"] == state:
            out[r[factor]].append(r["seconds"])
    return {p: sorted(v) for p, v in sorted(out.items())}


def factor_pairs(data, tool, state):
    """Какие пары (позиция инструмента, позиция состояния) встретились.

    ЭТО ГЛАВНАЯ ПРОВЕРКА ПЛАНА ЗАМЕРА. Если пар мало и каждая позиция
    состояния встречается ровно с одной позицией инструмента, два фактора
    неразличимы: разложение времени по одному из них в точности совпадает
    с разложением по другому, и приписать наблюдение какому-то одному
    нельзя.

    Первая версия сценария ровно так и промахнулась: порядок состояний
    брался со смещением на номер инструмента, и у uv «состояние на первой
    позиции» означало «uv запускается третьим». Числа были верные, вывод
    о причине — нет.
    """
    seen = collections.Counter()
    for r in data:
        if r["tool"] == tool and r["state"] == state:
            seen[(r["tool_position"], r["state_position"])] += 1
    return seen


def explained_by_position(data, tool, state, factor="state_position"):
    """Объясняется ли разброс в клетке позицией замера.

    Широкий разброс сам по себе — не приговор, если внутри каждой позиции
    его нет: тогда клетка не «нестабильна», а РАСПАДАЕТСЯ на позиции, и
    это содержательный результат, а не шум.

    Ровно так вышло у uv/warm: на первой позиции круга около 1,7 с, на
    последующих около 0,5 с, и внутри каждой позиции разброс копеечный.
    Кэш инструмента полон в обоих случаях — разница в страничном кэше ОС.
    """
    groups = by_position(data, tool, state, factor)
    if len(groups) < 2:
        return False, groups
    for values in groups.values():
        if len(values) < 2:
            continue
        if values[0] <= 0 or values[-1] / values[0] > SPREAD_LIMIT:
            return False, groups
    return True, groups


def sign_test(hits, total):
    """Двусторонний точный биномиальный тест на честной монете.

    Возвращает p — вероятность увидеть такой же или более однобокий счёт,
    если бы порядок определялся подбрасыванием монеты.
    """
    if total == 0:
        return 1.0
    k = max(hits, total - hits)
    tail = sum(math.comb(total, i) for i in range(k, total + 1))
    return min(1.0, 2 * tail / (2 ** total))


def ordering(data, state, first, second):
    """Покруговое сравнение двух инструментов в одном состоянии.

    ПОЧЕМУ ПОКРУГОВО, А НЕ ПО РАЗМАХАМ. Первая версия проверки объявляла
    порядок ненадёжным, если размах одной клетки налезал на размах другой.
    Размах — статистика по худшему одиночному чтению, и одного выброса
    хватает, чтобы забраковать порядок, который держится в семнадцати
    кругах из восемнадцати. Ровно это и случилось с pip/noop.

    Порядок — утверждение о том, кто быстрее, и проверять его надо тем же
    способом, каким стенд считает всё остальное: в каждом круге по
    отдельности, а потом знаком.
    """
    by_round = collections.defaultdict(dict)
    for r in data:
        if r["state"] == state and r["tool"] in (first, second):
            by_round[r["round"]][r["tool"]] = r["seconds"]
    hits = total = 0
    for pair in by_round.values():
        if first not in pair or second not in pair:
            continue
        total += 1
        if pair[first] < pair[second]:
            hits += 1
    return hits, total, sign_test(hits, total)


def spread_report(cells):
    """Делит клетки на «широкие» и «слишком быстрые, чтобы судить».

    Вынесено отдельно, потому что именно здесь легко соврать в обе
    стороны: применить порог ко всем клеткам — забраковать исправные
    замеры на сотых долях секунды; не применять вовсе — пропустить
    настоящую нестабильность.
    """
    wide, tiny = [], []
    for (tool, state), values in sorted(cells.items()):
        v = sorted(values)
        if not v or v[0] <= 0:
            continue
        ratio = v[-1] / v[0]
        if med(v) < SPREAD_FLOOR_S:
            tiny.append((tool, state, ratio, med(v)))
        elif ratio > SPREAD_LIMIT:
            wide.append((tool, state, ratio))
    return wide, tiny


def verdict(data):
    problems = []

    stale = [r for r in data if "sha256" not in r or "caches_before_mib" not in r]
    if stale:
        # Возврат немедленный. Продолжать разбор нечем: дальше идут
        # обращения к sha256 и caches_before_mib, которых в этих строках
        # и нет. Первая версия шла дальше и падала KeyError — отказ был
        # верным по существу, но вместо объяснения показывал трассировку,
        # а по трассировке причину «фикстура старая» не прочитать.
        problems.append(f"{len(stale)} строк без полей текущего сценария — "
                        + STALE)
        return problems

    hashes = {r["sha256"] for r in data}
    if len(hashes) != 1:
        problems.append(f"инструменты установили РАЗНОЕ ({len(hashes)} хешей) "
                        "— сравнивать их нельзя: быстрее окажется тот, кто "
                        "сделал меньше")

    if any(f not in r for r in data for f in FACTORS):
        problems.append(
            "в строках нет обеих позиций замера — фикстура снята версией "
            "сценария, где порядок инструментов и порядок состояний были "
            "связаны, и приписать наблюдение одному из них нельзя")
        return problems

    for factor in FACTORS:
        for key, by_pos in sorted(position_counts(data, factor).items()):
            got = [by_pos.get(p, 0) for p in range(3)]
            if max(got) - min(got) > 2:
                problems.append(
                    f"{key} распределён по «{factor}» неравномерно ({got}) — "
                    "ротация не сбалансирована")

    for tool in TOOLS:
        for state in STATES:
            pairs = factor_pairs(data, tool, state)
            if not pairs:
                continue
            by_state_pos = collections.defaultdict(set)
            for tool_pos, state_pos in pairs:
                by_state_pos[state_pos].add(tool_pos)
            if all(len(v) == 1 for v in by_state_pos.values()):
                problems.append(
                    f"{tool}/{state}: позиция состояния однозначно задаёт "
                    "позицию инструмента — два порядка не разведены, и "
                    "разложение по одному из них ничего не доказывает")

    for state in STATES:
        for first, second in itertools.combinations(TOOLS, 2):
            hits, total, p = ordering(data, state, first, second)
            if not total:
                continue
            if p > SIGN_ALPHA:
                faster = first if hits * 2 >= total else second
                problems.append(
                    f"{state}: порядок {first} и {second} не установлен — "
                    f"{faster} быстрее лишь в {max(hits, total - hits)} "
                    f"кругах из {total} (p={p:.2f}), это неотличимо от "
                    "монетки")

    dirty = [r for r in data if r["state"] == "cold"
             and r["caches_before_mib"].get(r["tool"], 0) > COLD_LIMIT_MIB]
    if dirty:
        worst = max(r["caches_before_mib"][r["tool"]] for r in dirty)
        problems.append(f"{len(dirty)} холодных прогонов начались с непустым "
                        f"кэшем (до {worst} МиБ) — измерен тёплый под видом "
                        "холодного")
    return problems


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
        cells[(r["tool"], r["state"])].append(r["seconds"])

    problems = verdict(data)
    if any(STALE in p for p in problems):
        # То же самое и здесь: печать заголовка сама лезет в поля.
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return 2

    print("=" * 78)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 78)
    print()
    print(f"1) различных хешей набора: {len({r['sha256'] for r in data})} "
          f"(обязан быть 1), пакетов: "
          f"{sorted({r['packages'] for r in data})}")
    cold_max = max((r["caches_before_mib"].get(r["tool"], 0)
                    for r in data if r["state"] == "cold"), default=0)
    print(f"2) наибольший кэш перед холодным прогоном: {cold_max} МиБ "
          f"(порог {COLD_LIMIT_MIB})")

    wide, tiny = spread_report(cells)
    print(f"3) клеток с разбросом выше {SPREAD_LIMIT}x: {len(wide)} из "
          f"{len(cells)}")
    for tool, state, ratio in wide:
        v = sorted(cells[(tool, state)])
        print(f"   {tool}/{state}: {ratio:.2f}x ({v[0]:.2f}-{v[-1]:.2f})")
    print("4) порядок инструментов по кругам (знаковый тест):")
    for state in STATES:
        parts = []
        for first, second in itertools.combinations(TOOLS, 2):
            hits, total, p = ordering(data, state, first, second)
            if not total:
                continue
            faster, count = ((first, hits) if hits * 2 >= total
                             else (second, total - hits))
            parts.append(f"{faster} быстрее {count}/{total}")
        print(f"   {state:<5} " + "; ".join(parts))
    if tiny:
        print(f"   не судятся по отношению (медиана ниже {SPREAD_FLOOR_S} с, "
              f"отношение говорит о таймере):")
        for tool, state, ratio, m in tiny:
            print(f"     {tool}/{state}: медиана {m:.3f} с, отношение "
                  f"{ratio:.2f}x")

    print()
    if problems:
        print("ВЕРДИКТ: НЕГОДЕН")
        for p in problems:
            print("  - " + p)
        return 2
    print("ВЕРДИКТ: замер годен")

    print()
    print("=" * 78)
    print("ВРЕМЯ УСТАНОВКИ, с (медиана по кругам и размах)")
    print("=" * 78)
    print()
    print(f"  {'инструмент':<12}", end="")
    for state in STATES:
        print(f"{state:>22}", end="")
    print()
    for tool in TOOLS:
        print(f"  {tool:<12}", end="")
        for state in STATES:
            v = sorted(cells.get((tool, state), []))
            if not v:
                print(f"{'—':>22}", end="")
                continue
            cell = f"{med(v):.2f} ({v[0]:.2f}-{v[-1]:.2f})"
            print(f"{cell:>22}", end="")
        print()

    print()
    print("  cold — кэш пуст, окружения нет: новая машина, первый прогон CI")
    print("  warm — кэш полон, окружения нет: CI с восстановленным кэшем")
    print("  noop — кэш и окружение на месте: повторный install у разработчика")

    print()
    print("=" * 78)
    print(f"ВО СКОЛЬКО РАЗ ДОЛЬШЕ, ЧЕМ {BASE} (покругово, медиана и размах)")
    print("=" * 78)
    print()
    print(f"  {'инструмент':<12}", end="")
    for state in STATES:
        print(f"{state:>22}", end="")
    print()
    for tool in TOOLS:
        if tool == BASE:
            continue
        print(f"  {tool:<12}", end="")
        for state in STATES:
            sh = paired(data, tool, state)
            cell = (f"{med(sh):.1f}x ({sh[0]:.1f}-{sh[-1]:.1f})"
                    if sh else "—")
            print(f"{cell:>22}", end="")
        print()

    print()
    print("=" * 78)
    print("ЧТО ДАЁТ ТЁПЛЫЙ КЭШ: cold минус warm, покругово")
    print("=" * 78)
    print()
    print("  Разность считается в одном круге — там состояние машины общее.")
    print("  Знак к выбросам нечувствителен: если кэш не помогает, он должен")
    print("  делиться примерно пополам.")
    print()
    print(f"  {'инструмент':<12}{'знак +':>9}{'медиана, с':>13}"
          f"{'межквартильный размах':>26}")
    for tool in TOOLS:
        d = cache_gain(data, tool)
        if not d:
            continue
        n = len(d)
        pos = sum(1 for x in d if x > 0)
        q1, q3 = d[n // 4], d[(3 * n) // 4]
        iqr = f"{q1:+.2f} .. {q3:+.2f}"
        print(f"  {tool:<12}{f'{pos}/{n}':>9}{med(d):>+13.2f}{iqr:>26}")

    print()
    print("=" * 78)
    print("ЭТО ЭФФЕКТ КЭША ИЛИ ЭФФЕКТ ПОЗИЦИИ?")
    print("=" * 78)
    print()
    print("  Тот же выигрыш, посчитанный отдельно по кругам, где холодный")
    print("  замер шёл РАНЬШЕ тёплого, и по кругам, где ПОЗЖЕ. Если бы")
    print("  разность создавалась прогревом машины, во второй колонке она")
    print("  обязана была бы упасть или сменить знак: там тёплый замер")
    print("  выполняется на менее прогретой машине, чем холодный.")
    print()
    print(f"  {'инструмент':<12}{'холодный раньше':>26}"
          f"{'тёплый раньше':>26}")
    for tool in TOOLS:
        cf = cache_gain(data, tool, "cold-first")
        wf = cache_gain(data, tool, "warm-first")
        if not cf and not wf:
            continue

        def cell(d):
            if not d:
                return "нет кругов"
            pos = sum(1 for x in d if x > 0)
            return f"{med(d):+.2f} с, знак {pos}/{len(d)}"

        print(f"  {tool:<12}{cell(cf):>26}{cell(wf):>26}")
    print()
    print("  Знак после запятой — размер группы. Он невелик, и выводы о")
    print("  ПРИЧИНАХ на нём не строятся: разделение показывает лишь,")
    print("  сохраняется ли знак выигрыша при обратном порядке.")
    print()
    print("=" * 78)
    print("ДВА ПОРЯДКА, РАЗЛОЖЕННЫЕ ПО ОТДЕЛЬНОСТИ")
    print("=" * 78)
    print()
    print("  Внутри круга есть два независимых порядка: каким по счёту идёт")
    print("  ИНСТРУМЕНТ и каким по счёту идёт СОСТОЯНИЕ внутри его тройки.")
    print("  Раскладывать время по одному из них имеет смысл, только если")
    print("  они разведены — иначе оба разложения совпадают, и приписать")
    print("  наблюдение какому-то одному фактору нельзя.")
    print()
    shown = False
    for tool in TOOLS:
        for state in STATES:
            cell = sorted(
                r["seconds"] for r in data
                if r["tool"] == tool and r["state"] == state)
            if not cell or cell[0] <= 0 or cell[-1] / cell[0] < SPREAD_LIMIT:
                continue
            shown = True
            print(f"  {tool}/{state} — разброс {cell[-1] / cell[0]:.2f}x, "
                  "по каждому фактору отдельно:")
            for factor, title in (("tool_position", "позиция инструмента"),
                                  ("state_position", "позиция состояния")):
                groups = by_position(data, tool, state, factor)
                text = "  ".join(f"{p + 1}-я: {med(v):.2f}"
                                 for p, v in groups.items())
                print(f"    {title:<22}{text}")
            print(f"    различных пар факторов: "
                  f"{len(factor_pairs(data, tool, state))} из 9")
    if not shown:
        print("  клеток с разбросом выше порога нет")

    print()
    print("  Сбалансированность (сколько раз на каждой позиции):")
    for factor, title in (("tool_position", "и"), ("state_position", "с")):
        for key, by_pos in sorted(position_counts(data, factor).items()):
            got = ", ".join(f"{p + 1}-я: {by_pos.get(p, 0)}"
                            for p in range(3))
            print(f"    {title}  {key:<8} {got}")

    net = pypi_rows()
    if net:
        print()
        print("=" * 78)
        print("ПЛЕЧО ПРОТИВ НАСТОЯЩЕГО PyPI — НЕВОСПРОИЗВОДИМО")
        print("=" * 78)
        print()
        print("  Числа ниже НЕ участвуют ни в одной таблице выше и не годятся")
        print("  для сравнения инструментов. PyPI отвечает по-разному в разное")
        print("  время, канал у каждого свой, зеркала различаются между собой")
        print("  сильнее, чем инструменты. Плечо показывает порядок величины —")
        print("  во сколько раз дороже та же установка, когда за пакетами надо")
        print("  идти в сеть.")
        print()
        measured = [r for r in net if r.get("measured")]
        local = {}
        for r in rows():
            if r["state"] == "cold":
                local.setdefault(r["tool"], []).append(r["seconds"])
        by_tool = {}
        for r in measured:
            by_tool.setdefault(r["tool"], []).append(r["seconds"])
        print(f"  {'инструмент':<12}{'по повторам, с':>26}"
              f"{'локально (медиана)':>22}")
        for tool in TOOLS:
            if tool not in by_tool:
                continue
            got = ", ".join(f"{v:.1f}" for v in by_tool[tool])
            base = med(local.get(tool, []))
            print(f"  {tool:<12}{got:>26}{base:>19.2f} с")
        for r in net:
            if not r.get("measured"):
                print(f"  {r['tool']:<12}{'не измеряется':>26}"
                      f"   {r['reason']}")
        print()
        spread = max((max(v) / min(v) for v in by_tool.values() if min(v) > 0),
                     default=1)
        print(f"  Разброс между повторами ОДНОЙ И ТОЙ ЖЕ команды в этом "
              f"прогоне: до {spread:.1f}x.")
        print("  Малый разброс внутри прогона НЕ делает плечо")
        print("  воспроизводимым, и полагаться на него нельзя: повторы идут")
        print("  подряд, в одних и тех же условиях сети. На этом стенде тот")
        print("  же замер в разных сеансах давал pip 18,6 и 60,8 секунды, а")
        print("  uv — 3,4 и 31,4. Сеть не контролируется, поэтому плечо")
        print("  годится на одно: показать порядок надбавки, а не сравнивать")
        print("  инструменты.")

    print()
    print("  ГРАНИЦА ВЫВОДА. Холодным считается кэш ИНСТРУМЕНТА, а не")
    print("  страничный кэш ОС: перед каждым состоянием выполняется")
    print("  одинаковый неучитываемый прогон, поэтому машина прогрета")
    print("  одинаково во всех трёх. Измеряется «первый install там, где")
    print("  пакетов ещё не скачивали», а не «install на только что")
    print("  загруженной машине» — это разные величины, и вторую этот стенд")
    print("  не даёт.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
