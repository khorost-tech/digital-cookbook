//! Домен: объёмная скидка по тирам.
//!
//! Тот же домен, что в стенде `architecture/testing/across-languages` (Go, Java,
//! Python) — чтобы Rust-приёмы можно было сравнить с соседями напрямую.

use crate::money::Money;

/// От 100.00 — 5%.
pub const TIER1_CENTS: i64 = 10_000;
/// От 200.00 — 10%.
pub const TIER2_CENTS: i64 = 20_000;

/// Размер скидки для суммы заказа.
///
/// ```
/// use rust_testing::{money::Money, pricing::discount};
/// let total = Money::try_new(10_000).unwrap();
/// assert_eq!(discount(total).cents(), 500);
/// ```
pub fn discount(total: Money) -> Money {
    let c = total.cents();
    let d = if c >= TIER2_CENTS {
        c * 10 / 100
    } else if c >= TIER1_CENTS {
        c * 5 / 100
    } else {
        0
    };
    // unwrap здесь безопасен и это видно локально: d ∈ [0, c], а c >= 0 по типу
    // Money. Тест «скидка не отрицательна» писать не на что — она не может.
    Money::try_new(d).expect("скидка неотрицательна по построению")
}

/// Итоговая цена: сумма минус скидка.
///
/// ```
/// use rust_testing::{money::Money, pricing::price};
/// // 250.00 минус 10%
/// assert_eq!(price(Money::try_new(25_000).unwrap()).cents(), 22_500);
/// ```
pub fn price(total: Money) -> Money {
    total.saturating_sub(discount(total))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn m(c: i64) -> Money {
        Money::try_new(c).unwrap()
    }

    // Табличный тест — в Rust это обычный цикл по массиву кортежей.
    // Ни фреймворка, ни макросов не нужно.
    #[test]
    fn скидка_по_тирам() {
        let cases = [
            (9_999, 0, "ниже порога"),
            (10_000, 500, "ровно 100.00 — 5%"),
            (19_999, 999, "на копейку ниже 200 — 5% с усечением"),
            (20_000, 2_000, "ровно 200.00 — 10%"),
            (25_000, 2_500, "250.00 — 10%"),
            (0, 0, "ноль"),
        ];
        for (total, want, name) in cases {
            assert_eq!(discount(m(total)).cents(), want, "случай: {name}");
        }
    }

    #[test]
    fn цена_это_сумма_минус_скидка() {
        assert_eq!(price(m(10_000)).cents(), 9_500);
        assert_eq!(price(m(25_000)).cents(), 22_500);
    }
}
