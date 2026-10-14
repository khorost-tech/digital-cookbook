"""Разбор: чего стоит держать одновременность.

Слово «дешевле» относится к ресурсам, а не к пропускной способности.

ПАМЯТЬ СЧИТАЕТСЯ ПО PSS, а не по сумме RSS. Сумма RSS считает общие страницы
заново в каждом процессе: у модели процессов, где 67 процессов делят
интерпретатор и страницы после fork, это завышает результат почти вчетверо.
PSS делит общие страницы между теми, кто их разделяет, и потому отвечает на
вопрос «сколько физической памяти занимает набор процессов».

Сумма RSS печатается рядом как справка: разрыв между ней и PSS показывает,
насколько велик двойной учёт. Плюс cgroup контейнера как независимая
перекрёстная проверка — она включает страничный кэш, поэтому чуть выше PSS,
но обязана быть с ней одного порядка. Если cgroup ближе к сумме RSS, чем к
PSS, значит с метрикой что-то не так.

Первым делом проверяется ЧИСТОТА замера. Каждое плечо живёт в собственном
процессе и печатает baseline_procs — сколько процессов python было видно в
момент его старта. Значение, отличное от 1, означает, что плечо мерило не
только себя: так первая версия сценария молча приписывала потокам хвост
forkserver, оставшийся от плеча процессов.
"""

import json
import os
import sys
from pathlib import Path

# Каталог фикстур переопределяется переменной окружения: на этом держится
# регрессионный тест предохранителей, см. test_analyze_footprint.py.
FIX = Path(os.environ.get("FIXTURES_DIR",
                          Path(__file__).resolve().parent.parent / "fixtures"))
MODES = ("asyncio", "threads", "processes")
SPREAD_LIMIT = 1.5


def rows(build):
    return [json.loads(l) for l in (FIX / f"footprint-{build}.txt")
            .read_text(encoding="utf-8").splitlines() if l.startswith("{")]


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def main():
    data = {b: rows(b) for b in ("gil", "nogil")}
    conc = data["gil"][0]["concurrency"]
    problems = []

    print("=" * 78)
    print("ЧИСТОТА ЗАМЕРА")
    print("=" * 78)
    print()
    for b, rs in data.items():
        if "baseline_procs" not in rs[0] or "peak_pss_mb" not in rs[0]:
            problems.append(f"{b}: фикстура старого формата, без baseline_procs/peak_pss_mb")
            print(f"  {b:<6} СТАРЫЙ ФОРМАТ: чистоту подтвердить нечем")
            continue
        dirty = [r for r in rs if r["baseline_procs"] != 1]
        print(f"  {b:<6} плеч всего {len(rs)}, начавших в грязной среде: {len(dirty)}")
        for r in dirty:
            problems.append(f"{b}/{r['mode']}/круг {r['round']}: "
                            f"на старте {r['baseline_procs']} процессов python")

    print()
    print("ВЕРДИКТ: старт чист" if not problems else "ВЕРДИКТ: ЗАГРЯЗНЁН")
    for p in problems:
        print("  - " + p)
    if problems:
        return 2

    print()
    print("=" * 78)
    print(f"СЛЕД ОДНОВРЕМЕННОСТИ: {conc} одновременных ожиданий")
    print("=" * 78)

    unstable = set()
    for b in ("gil", "nogil"):
        rs = data[b]
        print()
        print(f"сборка {b}:")
        print(f"  {'модель':<11}{'пик PSS':>11}{'разбр.':>8}{'сумма RSS':>12}"
              f"{'завыш.':>8}{'cgroup':>10}{'дескр.':>8}{'проц.':>7}{'ops/s':>9}")
        for m in MODES:
            sel = [r for r in rs if r["mode"] == m]
            v = [r["peak_pss_mb"] for r in sel]
            spread = max(v) / min(v)
            if spread > SPREAD_LIMIT:
                unstable.add((b, m))
            rss = med([r["peak_rss_sum_mb"] for r in sel])
            cg = med([r["peak_cgroup_mb"] for r in sel])
            print(f"  {m:<11}{med(v):>9.1f} МБ{spread:>7.2f}x{rss:>12.1f}"
                  f"{rss / med(v):>7.2f}x{cg:>10.1f}"
                  f"{med([r['peak_fds'] for r in sel]):>8.0f}"
                  f"{med([r['peak_procs'] for r in sel]):>7.0f}"
                  f"{med([r['ops_per_sec'] for r in sel]):>9.1f}")
            if cg > 0 and (cg / med(v) > 2.0 or med(v) / cg > 2.0):
                problems.append(f"{b}/{m}: cgroup {cg:.0f} и PSS {med(v):.0f} МБ "
                                "расходятся более чем вдвое — метрика под вопросом")

    print()
    print("=" * 78)
    print("ЧТО МОЖНО И ЧЕГО НЕЛЬЗЯ УТВЕРЖДАТЬ")
    print("=" * 78)
    print()
    if unstable:
        print(f"  Разброс выше {SPREAD_LIMIT}x, ранжировать по памяти нельзя: "
              + ", ".join(f"{b}/{m}" for b, m in sorted(unstable)))
        print()

    for b in ("gil", "nogil"):
        rs = data[b]
        vals = {m: med([r["peak_pss_mb"] for r in rs if r["mode"] == m]) for m in MODES}
        light = max(vals["asyncio"], vals["threads"])
        print(f"  {b}: процессы {vals['processes']:.1f} МБ против "
              f"{light:.1f} МБ у самой тяжёлой из лёгких моделей = "
              f"{vals['processes'] / light:.1f}x")
        hi, lo = max(vals["asyncio"], vals["threads"]), min(vals["asyncio"], vals["threads"])
        ratio = hi / lo
        if ratio < 1.15 or (b, "asyncio") in unstable or (b, "threads") in unstable:
            print(f"     asyncio против потоков: {vals['asyncio']:.1f} против "
                  f"{vals['threads']:.1f} МБ — различие в пределах шума, "
                  "ранжировать нельзя")
        else:
            light = "asyncio" if vals["asyncio"] < vals["threads"] else "потоки"
            print(f"     asyncio против потоков: {vals['asyncio']:.1f} против "
                  f"{vals['threads']:.1f} МБ, {light} легче в {ratio:.1f} раза")

    print()
    print("  Кросс-сборочное сравнение (разброс на обеих сборках позволяет):")
    for m in MODES:
        g = med([r["peak_pss_mb"] for r in data["gil"] if r["mode"] == m])
        n = med([r["peak_pss_mb"] for r in data["nogil"] if r["mode"] == m])
        if (("gil", m) in unstable) or (("nogil", m) in unstable):
            print(f"    {m:<11} нельзя: разброс велик")
        else:
            print(f"    {m:<11} {g:.1f} -> {n:.1f} МБ = {n / g:.1f}x на free-threaded")

    # Проблемы, найденные в разделе таблиц (расхождение cgroup и PSS),
    # набираются в тот же список ПОСЛЕ первой проверки. Без этой печати и
    # ненулевого кода предохранитель был бы мёртвым: он взводился, но ни на
    # что не влиял. Так и было, пока внешнее ревью это не поймало.
    if problems:
        print()
        print("=" * 78)
        print("ВЕРДИКТ: МЕТРИКА ПОД ВОПРОСОМ")
        print("=" * 78)
        for p in problems:
            print("  - " + p)
        return 3

    return 0


if __name__ == "__main__":
    sys.exit(main())
