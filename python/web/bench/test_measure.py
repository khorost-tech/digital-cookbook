"""Тесты измерителя. Те же, что в стенде python/concurrency: дублирование
намеренное, стенды самодостаточны."""

from measure import percentile, latin_square


def test_percentile_nearest_rank():
    # На выборке из 10 точек p95 и p99 обязаны указывать на десятую:
    # формула floor((n-1)*q) занижает верхние перцентили на позицию.
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
        for r in range(1, 12):
            for k in (3, 5):
                assert sorted(latin_square(seed, r, k)) == list(range(k))


def test_latin_square_is_balanced():
    # За k последовательных кругов каждая цель обязана занять каждую позицию
    # ровно один раз. Иначе ротация не убирает смещение порядка, а маскирует.
    for k in (3, 5):
        for seed in (0, 7, 1337):
            seen = [{} for _ in range(k)]
            for r in range(1, k + 1):
                for pos, arm in enumerate(latin_square(seed, r, k)):
                    seen[pos][arm] = seen[pos].get(arm, 0) + 1
            for pos in range(k):
                assert sorted(seen[pos].values()) == [1] * k, (
                    f"k={k} seed={seed}: позиция {pos} несбалансирована")
