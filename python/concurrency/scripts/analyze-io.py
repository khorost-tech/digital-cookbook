"""Разбор фикстур IO-сценария.

Главный вопрос здесь другой, чем в CPU-сценарии. На ожидании GIL не должен
быть узким местом, поэтому сравнение сборок не так интересно. Интересно,
сколько одновременных ожиданий модель тянет и какой ценой.

Поэтому основная метрика — не голые ops/s, а доля от теоретического потолка.
При одновременности w и ожидании d секунд идеальная модель выдаёт w/d
операций в секунду. Отставание от этого потолка и есть накладные расходы
модели, выраженные в понятной величине.
"""

import json
import sys
from pathlib import Path

FIX = Path(__file__).resolve().parent.parent / "fixtures"


def load(build):
    path = FIX / f"io-{build}.txt"
    rows, skipped, delay = [], [], None
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line.startswith("# ПРОПУЩЕНО"):
            skipped.append(line[2:])
        elif line.startswith("# seed="):
            for part in line[2:].split():
                if part.startswith("delay="):
                    delay = float(part[len("delay="):].rstrip("s"))
        elif line and not line.startswith("#"):
            rows.append(json.loads(line))
    return rows, skipped, delay


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def main():
    data, skips, delays = {}, {}, {}
    for b in ("gil", "nogil"):
        data[b], skips[b], delays[b] = load(b)
    delay = delays["gil"]
    if delay is None:
        print("ОТКАЗ: в шапке фикстуры нет delay — потолок не посчитать", file=sys.stderr)
        return 2

    modes = sorted({r["mode"] for r in data["gil"]})
    workers = sorted({r["workers"] for r in data["gil"]})
    problems = []

    print("=" * 74)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 74)
    print()

    degenerate = [r for rows in data.values() for r in rows if r["p99_ms"] == r["p50_ms"]]
    total = sum(len(v) for v in data.values())
    print(f"1) p99 == p50: {len(degenerate)} прогонов из {total}")
    if degenerate:
        problems.append("вырожденные перцентили: выборка недостаточна")

    # Ожидание обязано состояться: медиана операции не может быть меньше
    # заказанной задержки. Это проверяет, что мерили ожидание, а не пустоту.
    short = [r for rows in data.values() for r in rows
             if r["p50_ms"] < delay * 1000 * 0.9]
    print(f"2) операций короче заказанного ожидания ({delay * 1000:.0f} мс): {len(short)}")
    if short:
        problems.append("ожидание не состоялось — измерялась не IO-нагрузка")

    # На ожидании GIL не должен быть узким местом: сборки обязаны идти вровень.
    worst = (0.0, None)
    for m in modes:
        for w in workers:
            g = [r["ops_per_sec"] for r in data["gil"]
                 if r["mode"] == m and r["workers"] == w]
            n = [r["ops_per_sec"] for r in data["nogil"]
                 if r["mode"] == m and r["workers"] == w]
            if not g or not n:
                continue
            ratio = med(n) / med(g)
            dev = abs(ratio - 1.0)
            if dev > worst[0]:
                worst = (dev, f"{m}/w={w}: {ratio:.2f}x")
    print(f"3) наибольшее расхождение сборок на IO: {worst[1]}")
    if worst[0] > 0.25:
        problems.append("сборки сильно расходятся на IO — GIL здесь не должен решать")

    print()
    print("ВЕРДИКТ: замер годен" if not problems else "ВЕРДИКТ: НЕГОДЕН")
    for p in problems:
        print("  - " + p)

    for b in ("gil", "nogil"):
        if skips[b]:
            print()
            print(f"пропущено по потолку процессов ({b}):")
            for s in sorted(set(skips[b])):
                print("  " + s)

    for b in ("gil", "nogil"):
        print()
        print("=" * 74)
        print(f"СБОРКА {b}: ops/s (медиана кругов) и доля от потолка w/{delay}s")
        print("=" * 74)
        header = "  модель      "
        for w in workers:
            header += f"{('w=' + str(w)):>17}"
        print(header)
        for m in modes:
            row = f"  {m:<12}"
            for w in workers:
                v = [r["ops_per_sec"] for r in data[b]
                     if r["mode"] == m and r["workers"] == w]
                if not v:
                    row += f"{'—':>17}"
                    continue
                ceiling = w / delay
                row += f"{med(v):10.1f} ({med(v) / ceiling * 100:3.0f}%)"
            print(row)
        print("  в скобках — доля от теоретического потолка при этой одновременности")

    print()
    print("=" * 74)
    print("УСТОЙЧИВОСТЬ: разброс ops/s между кругами одной конфигурации")
    print("=" * 74)
    print("  Конфигурация с разбросом больше 1.5x не годится для ранжирования")
    print("  моделей: в ней любое единичное число — лотерея, а не измерение.")
    print()
    unstable = []
    for b in ("gil", "nogil"):
        for m in modes:
            for w in workers:
                v = [r["ops_per_sec"] for r in data[b]
                     if r["mode"] == m and r["workers"] == w]
                if len(v) < 2:
                    continue
                spread = max(v) / min(v)
                if spread > 1.5:
                    unstable.append((b, m, w, spread, min(v), max(v)))
    if not unstable:
        print("  неустойчивых конфигураций нет")
    else:
        for b, m, w, spread, lo, hi in unstable:
            print(f"  {b}/{m}/w={w}: разброс {spread:.2f}x ({lo:.0f}..{hi:.0f} ops/s)")
        print()
        print("  По этим конфигурациям сравнивать модели и сборки НЕЛЬЗЯ.")
        print("  Прицельная проверка io-hi (15 кругов вместо 3) показала, что",)
        print("  при 256 потоках пропускная способность гуляет более чем вдвое,")
        print("  а различие сборок там неразличимо (p=0.15).")

    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
