"""Тесты измерителя. Обязаны проходить на ОБЕИХ сборках Python: измеритель
общий для плеч, и расхождение в нём испортило бы сравнение сборок."""

import time

from measure import percentile, latin_square
from workload import cpu_task, io_task


def test_percentile_nearest_rank():
    # Метод nearest-rank: idx = ceil(q*n) - 1. На выборке из 10 точек p95 и p99
    # обязаны указывать на десятую, а не на девятую: формула floor((n-1)*q)
    # занижает верхние перцентили на позицию.
    xs = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]
    assert percentile(xs, 0.50) == 5
    assert percentile(xs, 0.95) == 10
    assert percentile(xs, 0.99) == 10


def test_percentile_edges():
    assert percentile([], 0.5) == 0
    assert percentile([42], 0.99) == 42


def test_percentile_does_not_mutate_input():
    xs = [3, 1, 2]
    percentile(xs, 0.5)
    assert xs == [3, 1, 2], "перцентиль испортил список вызывающего"


def test_latin_square_is_a_permutation():
    for seed in (0, 7, 1337):
        for r in range(1, 10):
            order = latin_square(seed, r, 3)
            assert sorted(order) == [0, 1, 2], f"не перестановка: {order}"


def test_latin_square_is_balanced():
    # За k последовательных кругов каждое плечо обязано занять каждую позицию
    # ровно один раз. Иначе ротация не убирает систематическое смещение
    # порядка, а лишь маскирует его.
    k = 3
    for seed in (0, 7, 1337):
        seen = [{} for _ in range(k)]
        for r in range(1, k + 1):
            for pos, arm in enumerate(latin_square(seed, r, k)):
                seen[pos][arm] = seen[pos].get(arm, 0) + 1
        for pos in range(k):
            assert sorted(seen[pos].values()) == [1] * k, (
                f"seed={seed}: позиция {pos} несбалансирована: {seen[pos]}"
            )


def test_cpu_task_is_deterministic():
    # Если задача недетерминирована, разные плечи считают разное,
    # и сравнивать их скорость бессмысленно.
    assert cpu_task(5000) == cpu_task(5000)


def test_cpu_task_depends_on_size():
    assert cpu_task(1000) != cpu_task(2000)


def test_io_task_actually_waits():
    t0 = time.perf_counter()
    io_task(0.05)
    assert time.perf_counter() - t0 >= 0.04
