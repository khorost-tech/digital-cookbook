"""Разбор фикстур CPU-сценария.

Считает выигрыш и цену, но сначала проверяет три признака негодного замера.
Числа для статьи берутся из вывода этого скрипта, а не из глаза.
"""

import json
import sys
from pathlib import Path

FIX = Path(__file__).resolve().parent.parent / "fixtures"


def load(build):
    rows = []
    for line in (FIX / f"cpu-{build}.txt").read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            rows.append(json.loads(line))
    return rows


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def main():
    data = {b: load(b) for b in ("gil", "nogil")}
    modes = sorted({r["mode"] for r in data["gil"]})
    workers = sorted({r["workers"] for r in data["gil"]})
    problems = []

    print("=" * 70)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 70)

    # 1. Вырожденные перцентили: выборка мала.
    degenerate = [r for rows in data.values() for r in rows if r["p99_ms"] == r["p50_ms"]]
    print(f"\n1) p99 == p50: {len(degenerate)} прогонов из "
          f"{sum(len(v) for v in data.values())}")
    if degenerate:
        problems.append("вырожденные перцентили: выборка недостаточна")

    # 2. Дрейф между кругами одной конфигурации.
    print("\n2) разброс ops/s между кругами одной конфигурации:")
    worst = (0, None)
    for b, rows in data.items():
        for m in modes:
            for w in workers:
                v = [r["ops_per_sec"] for r in rows if r["mode"] == m and r["workers"] == w]
                if len(v) < 2:
                    continue
                spread = max(v) / min(v)
                if spread > worst[0]:
                    worst = (spread, f"{b}/{m}/w={w}")
    print(f"   максимум: {worst[0]:.2f}x ({worst[1]})")
    if worst[0] > 1.30:
        problems.append(f"дрейф между кругами {worst[0]:.2f}x — машина нестабильна")

    # 3. Главная проверка: на сборке С GIL потоки НЕ должны масштабироваться
    #    на CPU-нагрузке. Если масштабируются — задача не CPU-bound.
    g1 = med([r["ops_per_sec"] for r in data["gil"]
              if r["mode"] == "threads" and r["workers"] == 1])
    g8 = med([r["ops_per_sec"] for r in data["gil"]
              if r["mode"] == "threads" and r["workers"] == max(workers)])
    print(f"\n3) сборка с GIL, потоки 1 -> {max(workers)}: "
          f"{g1:.1f} -> {g8:.1f} ops/s = {g8 / g1:.2f}x")
    if g8 / g1 > 1.30:
        problems.append(
            f"под GIL потоки дали {g8 / g1:.2f}x на CPU-нагрузке — "
            "задача не удерживает GIL, замер меряет не то, что заявлено")

    print("\n" + ("ВЕРДИКТ: замер годен" if not problems else "ВЕРДИКТ: НЕГОДЕН"))
    for p in problems:
        print("  - " + p)

    print("\n" + "=" * 70)
    print("МАСШТАБИРОВАНИЕ: ops/s (медиана кругов), в скобках — к одному воркеру")
    print("=" * 70)
    for b in ("gil", "nogil"):
        print(f"\nсборка {b}:")
        print("  модель      " + "".join(f"{('w=' + str(w)):>18}" for w in workers))
        for m in modes:
            base = med([r["ops_per_sec"] for r in data[b]
                        if r["mode"] == m and r["workers"] == 1])
            cells = []
            for w in workers:
                v = med([r["ops_per_sec"] for r in data[b]
                         if r["mode"] == m and r["workers"] == w])
                cells.append(f"{v:9.1f} ({v / base:4.2f}x)")
            print(f"  {m:<12}" + "".join(f"{c:>18}" for c in cells))

    print("\n" + "=" * 70)
    print("ЦЕНА FREE-THREADING: nogil / gil при ОДИНАКОВОЙ модели и числе воркеров")
    print("=" * 70)
    print("  модель      " + "".join(f"{('w=' + str(w)):>12}" for w in workers))
    for m in modes:
        cells = []
        for w in workers:
            g = med([r["ops_per_sec"] for r in data["gil"]
                     if r["mode"] == m and r["workers"] == w])
            n = med([r["ops_per_sec"] for r in data["nogil"]
                     if r["mode"] == m and r["workers"] == w])
            cells.append(f"{n / g:.2f}x")
        print(f"  {m:<12}" + "".join(f"{c:>12}" for c in cells))
    print()
    print("  ВНИМАНИЕ: строку w=1 нельзя читать как чистую цену free-threading.")
    print("  Проверено probe_threads.py: при workers=1 в родительском процессе")
    print("  живёт 1 поток у asyncio, 2 у потоков и 3 у процессов. Отношение у")
    print("  потоков и процессов смешивает цену интерпретатора с накладными")
    print("  расходами исполнителя и, по гипотезе, с платой сборки с GIL за его")
    print("  передачу между потоками. Чистую цену интерпретатора изолирует только")
    print("  asyncio — см. analyze-cost.py.")

    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
