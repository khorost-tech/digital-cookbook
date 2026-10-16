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


def test_counterbalance_sets_relative_order():
    from measure import counterbalance
    order = [0, 1, 2, 3, 4]
    # flip=False: третий обязан идти раньше четвёртого.
    assert counterbalance(order, 3, 4, flip=False).index(3) < \
        counterbalance(order, 3, 4, flip=False).index(4)
    # flip=True: наоборот.
    assert counterbalance(order, 3, 4, flip=True).index(4) < \
        counterbalance(order, 3, 4, flip=True).index(3)


def test_counterbalance_keeps_every_element():
    from measure import counterbalance
    order = [2, 0, 4, 1, 3]
    assert sorted(counterbalance(order, 3, 4, flip=True)) == [0, 1, 2, 3, 4]


def test_cyclic_shift_alone_does_not_balance_the_pair():
    # Фиксируем причину, по которой понадобилась контрбалансировка: сам по
    # себе сдвиг оставляет пару почти в одном порядке.
    from measure import latin_square
    first = sum(
        1 for r in range(1, 12)
        for idx in range(5)
        if latin_square(idx, r, 5).index(3) < latin_square(idx, r, 5).index(4)
    )
    assert first >= 40, first          # из 55 сочетаний — подавляющее большинство


def test_counterbalanced_pair_is_split_across_rounds():
    from measure import counterbalance, latin_square
    for idx in range(5):
        rows_first = 0
        for r in range(1, 12):
            order = counterbalance(latin_square(idx, r, 5), 3, 4,
                                   flip=bool((r + idx) % 2))
            if order.index(3) < order.index(4):
                rows_first += 1
        # Одиннадцать кругов делятся как 5 и 6 при любом плече.
        assert rows_first in (5, 6), (idx, rows_first)
