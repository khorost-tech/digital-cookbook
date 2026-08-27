"""ПРИЁМ 2: property-based на Hypothesis — тот же сюжет, что в Go (rapid) и
Java (jqwik): инвариант держится; наивная монотонность находит нарушение
НЕНАДЁЖНО (в наших 20 прогонах на дефолте — ни разу, но это «редко», а не
«никогда»); доменный генератор находит стабильно и ужимает до границы тира.
Минимальность ужатого примера при этом локальная.

Серию прогонов гоняет ../property-matrix.sh — он же чистит кэш находок
(.hypothesis), без чего замер показывает «помнил», а не «нашёл заново».
"""

import os

import pytest
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

import pricing

# Бюджет наивного демо задаётся снаружи, чтобы воспроизводить обе колонки замера
# из README без правки кода. По умолчанию — дефолт Hypothesis (100 примеров):
#   pytest -m property_demo -k наивный                     → 100 примеров
#   PROP_EXAMPLES=20000 pytest -m property_demo -k наивный → 20 000 примеров
NAIVE_EXAMPLES = int(os.environ.get("PROP_EXAMPLES", "100"))


# Инвариант: скидка неотрицательна и не больше суммы. Держится.
@given(total=st.integers(min_value=0, max_value=1_000_000_000))
def test_скидка_в_разумных_границах(total: int) -> None:
    d = pricing.discount(total)
    assert d >= 0, f"скидка отрицательна: discount({total}) = {d}"
    assert d <= total, f"скидка больше суммы: discount({total}) = {d}"
    assert pricing.price(total) >= 0, f"итоговая цена отрицательна: price({total})"


def _monotonic(a: int, b: int) -> None:
    """Свойство: заказ на большую сумму не может стоить дешевле."""
    if a > b:
        a, b = b, a
    assert pricing.price(a) <= pricing.price(b), (
        f"заказ на {a} стоит {pricing.price(a)}, а на {b} — {pricing.price(b)}: "
        f"заплатить больше оказалось дешевле"
    )


# Наивный генератор: «просто дай числа из диапазона». Нарушение занимает узкую
# полосу входов, и генератор попадает туда редко — property-based не магия.
# На дефолтных 100 примерах Hypothesis не нашёл его ни разу за 20 прогонов;
# это «редко», а не «никогда».
#   pytest -m property_demo -k наивный
@pytest.mark.property_demo
@settings(max_examples=NAIVE_EXAMPLES, deadline=None, suppress_health_check=[HealthCheck.too_slow])
@given(
    a=st.integers(min_value=0, max_value=30_000),
    b=st.integers(min_value=0, max_value=30_000),
)
def test_монотонность_наивный_генератор(a: int, b: int) -> None:
    _monotonic(a, b)


# Генератор знает СТРУКТУРУ домена (у скидки есть пороги), но не знает про баг:
# ответ мы не подсказываем, нарушение по-прежнему находит свойство.
#   pytest -m property_demo -k доменный
около_тиров = st.one_of(
    st.integers(min_value=0, max_value=30_000),
    st.integers(min_value=pricing.TIER1_CENTS - 500, max_value=pricing.TIER1_CENTS + 500),
    st.integers(min_value=pricing.TIER2_CENTS - 500, max_value=pricing.TIER2_CENTS + 500),
)


@pytest.mark.property_demo
@given(a=около_тиров, b=около_тиров)
def test_монотонность_доменный_генератор(a: int, b: int) -> None:
    _monotonic(a, b)
