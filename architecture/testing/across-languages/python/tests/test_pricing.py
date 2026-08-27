"""ПРИЁМ 1: табличный тест. В Go — литерал слайса, в Java — @ParameterizedTest,
в Python — @pytest.mark.parametrize. Идиома разная, суть одна: примеры выбирает
человек, поэтому проверено ровно то, о чём человек подумал."""

import pytest

import pricing


@pytest.mark.parametrize(
    ("total", "want"),
    [
        pytest.param(9_999, 0, id="ниже порога"),
        pytest.param(10_000, 500, id="ровно 100.00 — 5%"),
        pytest.param(19_999, 999, id="на копейку ниже 200 — 5% с усечением"),
        pytest.param(20_000, 2_000, id="ровно 200.00 — 10%"),
        pytest.param(25_000, 2_500, id="250.00 — 10%"),
        pytest.param(0, 0, id="ноль"),
    ],
)
def test_discount(total: int, want: int) -> None:
    assert pricing.discount(total) == want
