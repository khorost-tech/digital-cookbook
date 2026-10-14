"""Разбор: пул потоков против пула процессов в run_in_executor.

Оба исполнителя меряются симметрично — одна работа, одна обвязка asyncio,
различается только класс пула. Без второго плеча вывод «потоки дешевле
процессов» не следовал бы из замера: сравнивать было бы не с чем.
"""

import json
import sys
from pathlib import Path

FIX = Path(__file__).resolve().parent.parent / "fixtures"
KINDS = ("threads", "processes")


def rows(build):
    return [json.loads(l) for l in (FIX / f"executor-{build}.txt")
            .read_text(encoding="utf-8").splitlines() if l.startswith("{")]


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def main():
    data = {b: rows(b) for b in ("gil", "nogil")}
    workers = sorted({r["workers"] for r in data["gil"]})
    problems = []

    print("=" * 76)
    print("ПРИЗНАКИ НЕГОДНОГО ЗАМЕРА")
    print("=" * 76)
    print()
    for b, rs in data.items():
        sums = {r["checksum"] for r in rs}
        gil = {r["gil_enabled"] for r in rs}
        print(f"  {b:<6} контрольных сумм различных: {len(sums)}, gil_enabled: {sorted(gil)}")
        if len(sums) != 1:
            problems.append(f"{b}: исполнители посчитали разное")
        if (b == "nogil") != (gil == {False}):
            problems.append(f"{b}: фактическое состояние GIL не соответствует плечу")

    worst = (0.0, None)
    for b, rs in data.items():
        for k in KINDS:
            for w in workers:
                v = [r["ops_per_sec"] for r in rs if r["executor"] == k and r["workers"] == w]
                if len(v) > 1 and max(v) / min(v) > worst[0]:
                    worst = (max(v) / min(v), f"{b}/{k}/w={w}")
    print(f"  максимальный разброс между кругами: {worst[0]:.2f}x ({worst[1]})")
    if worst[0] > 1.5:
        problems.append(f"разброс {worst[0]:.2f}x — конфигурации непригодны для ранжирования")

    print()
    print("ВЕРДИКТ: замер годен" if not problems else "ВЕРДИКТ: НЕГОДЕН")
    for p in problems:
        print("  - " + p)

    print()
    print("=" * 76)
    print("ПРОПУСКНАЯ СПОСОБНОСТЬ, ops/s (медианы кругов)")
    print("=" * 76)
    print()
    head = f"  {'воркеров':<10}"
    for b in ("gil", "nogil"):
        for k in KINDS:
            head += f"{(b + '/' + k):>18}"
    print(head)
    for w in workers:
        line = f"  {w:<10}"
        for b in ("gil", "nogil"):
            for k in KINDS:
                line += f"{med([r['ops_per_sec'] for r in data[b] if r['executor'] == k and r['workers'] == w]):>18.1f}"
        print(line)

    print()
    print("=" * 76)
    print("ЧТО ИЗ ЭТОГО СЛЕДУЕТ")
    print("=" * 76)
    print()
    top = max(workers)
    for b in ("gil", "nogil"):
        t = med([r["ops_per_sec"] for r in data[b] if r["executor"] == "threads" and r["workers"] == top])
        p = med([r["ops_per_sec"] for r in data[b] if r["executor"] == "processes" and r["workers"] == top])
        ratio = t / p
        if abs(ratio - 1) < 0.15:
            verdict = "по пропускной способности РАВНЫ (различие в пределах разброса)"
        elif ratio > 1:
            verdict = f"потоки быстрее процессов в {ratio:.2f}x"
        else:
            verdict = f"процессы быстрее потоков в {1 / ratio:.2f}x"
        print(f"  {b:<6} при {top} воркерах: потоки {t:.1f}, процессы {p:.1f} ops/s — {verdict}")
    print()
    print("  Замер отвечает только про пропускную способность. Про расход")
    print("  памяти и дескрипторов — отдельный сценарий run-footprint.sh:")
    print("  утверждать «дешевле» по этой таблице нельзя.")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
