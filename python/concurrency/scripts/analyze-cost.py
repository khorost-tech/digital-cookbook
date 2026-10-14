"""Цена free-threading при одном воркере.

ВАЖНО, какое плечо что измеряет. probe_threads.py показывает, что при
workers=1 в родительском процессе живёт 1 поток у asyncio, 2 у потоков и 3 у
процессов. Значит чистую цену интерпретатора изолирует ТОЛЬКО asyncio: он
однопоточен. У потоков и процессов сборка с GIL платит ещё и за передачу GIL
между собственными потоками исполнителя, поэтому их отношение — не цена
free-threading, а сумма двух разных эффектов с разными знаками.

Скрипт не просто делит медианы: он проверяет, различимо ли различие на фоне
разброса между кругами. Эффект порядка 10% на шумной машине может быть
неразрешим, и тогда честный ответ — "не разрешили", а не число.
"""

import json
import random
import sys
from pathlib import Path

FIX = Path(__file__).resolve().parent.parent / "fixtures"
RESAMPLES = 20000
SEED = 20260822          # фиксирован: результат теста воспроизводим


def load(build):
    path = FIX / f"cpu-cost-{build}.txt"
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            rows.append(json.loads(line))
    return rows


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def permutation_p(a, b, rng):
    """Двусторонний перестановочный тест на разность медиан.

    Гипотеза, которую пытаемся отвергнуть: обе выборки пришли из одного
    распределения, а наблюдаемая разница — перестановка ярлыков. Тест
    непараметрический: о форме распределения ops/s ничего не предполагаем.
    """
    observed = abs(med(a) - med(b))
    pool = list(a) + list(b)
    n = len(a)
    hits = 0
    for _ in range(RESAMPLES):
        rng.shuffle(pool)
        if abs(med(pool[:n]) - med(pool[n:])) >= observed:
            hits += 1
    return (hits + 1) / (RESAMPLES + 1)


def main():
    rng = random.Random(SEED)
    data = {b: load(b) for b in ("gil", "nogil")}
    modes = sorted({r["mode"] for r in data["gil"]})

    for b, rows in data.items():
        bad = [r for r in rows if r["workers"] != 1]
        if bad:
            print(f"ОТКАЗ: в {b} есть прогоны не при одном воркере", file=sys.stderr)
            return 2

    print("=" * 78)
    print(f"ЦЕНА FREE-THREADING ПРИ ОДНОМ ВОРКЕРЕ ({len(data['gil']) // len(modes)} кругов на модель)")
    print("=" * 78)
    print(f"{'модель':<11}{'gil ops/s':>22}{'nogil ops/s':>22}{'nogil/gil':>11}{'p':>9}")

    verdicts = []
    for m in modes:
        g = [r["ops_per_sec"] for r in data["gil"] if r["mode"] == m]
        n = [r["ops_per_sec"] for r in data["nogil"] if r["mode"] == m]
        ratio = med(n) / med(g)
        p = permutation_p(g, n, rng)
        print(f"{m:<11}"
              f"{med(g):9.1f} [{min(g):6.1f};{max(g):6.1f}]"
              f"{med(n):9.1f} [{min(n):6.1f};{max(n):6.1f}]"
              f"{ratio:10.3f}x{p:9.4f}")
        verdicts.append((m, ratio, p))

    print("\nв квадратных скобках — минимум и максимум по кругам")
    print("p — перестановочный тест на разность медиан, 20000 перестановок,")
    print("   seed фиксирован; p < 0.05 считаем различимым эффектом")

    print("\n" + "=" * 78)
    print("ЧТО ИЗ ЭТОГО СЛЕДУЕТ")
    print("=" * 78)
    print("  Цену интерпретатора читает только строка asyncio: это единственное")
    print("  однопоточное плечо. Строки threads и processes измеряют цену")
    print("  интерпретатора ВМЕСТЕ с платой за передачу GIL между потоками")
    print("  исполнителя, и знак у этих эффектов разный.")
    print("")
    for m, ratio, p in verdicts:
        if p >= 0.05:
            print(f"  {m:<11} различие НЕ РАЗРЕШЕНО (p={p:.3f}): на этой выборке")
            print(f"  {'':<11} нельзя утверждать ни платы, ни её отсутствия")
        else:
            pct = (ratio - 1) * 100
            word = "быстрее" if pct > 0 else "медленнее"
            print(f"  {m:<11} различие различимо (p={p:.3f}): "
                  f"сборка без GIL на {abs(pct):.1f}% {word}")


    print()
    print("=" * 78)
    print("ПРОВЕРКА МЕХАНИЗМА: сколько стоит ВТОРОЙ ПОТОК внутри одной сборки")
    print("=" * 78)
    print("  Сравнение asyncio (1 поток) с threads при workers=1 (2 потока)")
    print("  внутри ОДНОЙ сборки: цена интерпретатора здесь сокращается,")
    print("  остаётся только эффект появления второго потока.")
    print()
    for b in ("gil", "nogil"):
        rows = data[b]
        av = [r["ops_per_sec"] for r in rows if r["mode"] == "asyncio"]
        tv = [r["ops_per_sec"] for r in rows if r["mode"] == "threads"]
        pa = permutation_p(av, tv, rng)
        print(f"  {b:<6} {med(av):6.1f} -> {med(tv):6.1f} ops/s = "
              f"{med(tv) / med(av):.3f}x  (p={pa:.4f})")
    print()
    print("  Если второй поток дорог на сборке с GIL и бесплатен без него,")
    print("  то различие в плечах threads и processes объясняется передачей")
    print("  GIL, а не тем, что free-threading ускоряет однопоточный код.")

    return 0


if __name__ == "__main__":
    sys.exit(main())
