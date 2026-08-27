"""Доменное ядро — тот же код, что в go/pricing и java Pricing.

Объёмная скидка по тирам, всё в копейках.
"""

TIER1_CENTS = 10_000  # от 100.00 — 5%
TIER2_CENTS = 20_000  # от 200.00 — 10%


def discount(total_cents: int) -> int:
    """Размер скидки в копейках для суммы заказа."""
    if total_cents >= TIER2_CENTS:
        return total_cents * 10 // 100
    if total_cents >= TIER1_CENTS:
        return total_cents * 5 // 100
    return 0


def price(total_cents: int) -> int:
    """Итоговая цена: сумма минус скидка."""
    return total_cents - discount(total_cents)
