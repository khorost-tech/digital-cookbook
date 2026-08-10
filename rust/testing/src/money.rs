//! Деньги как тип, а не как число.
//!
//! Здесь начинается главное отличие Rust от Go/Java/Python в тестировании: часть
//! проверок тут не пишется тестом, потому что неверное состояние невозможно
//! построить — компилятор не даст. Конструктор один, он приватный по сути
//! (через `try_new`), и мимо него значения не появляются.

use thiserror::Error;

#[derive(Debug, Error, PartialEq, Eq)]
pub enum MoneyError {
    #[error("сумма отрицательна: {0} коп.")]
    Negative(i64),
    #[error("сумма больше максимума домена: {0} коп. (максимум {max} коп.)", max = Money::MAX_CENTS)]
    TooLarge(i64),
}

/// Сумма в копейках. Неотрицательна ПО ПОСТРОЕНИЮ.
///
/// Поле приватное, конструктор только через [`Money::try_new`] — поэтому
/// «а что если отрицательная сумма» перестаёт быть вопросом к тестам и
/// становится вопросом к компилятору.
///
/// ```
/// use rust_testing::money::Money;
/// let m = Money::try_new(10_000).unwrap();
/// assert_eq!(m.cents(), 10_000);
/// ```
///
/// Ни отрицательную, ни абсурдно большую не построить — вернётся ошибка, а не
/// паника:
///
/// ```
/// use rust_testing::money::{Money, MoneyError};
/// assert_eq!(Money::try_new(-1), Err(MoneyError::Negative(-1)));
/// assert_eq!(Money::try_new(i64::MAX), Err(MoneyError::TooLarge(i64::MAX)));
/// ```
///
/// А вот так — не скомпилируется вовсе: поле приватное, и обойти конструктор
/// нельзя. Этот doc-тест ПРОВЕРЯЕТ, что дыры нет:
///
/// ```compile_fail
/// use rust_testing::money::Money;
/// let m = Money(-500); // поле приватное → ошибка компиляции
/// ```
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub struct Money(i64);

impl Money {
    /// Максимум домена: 10 миллионов рублей. Заказов больше не бывает, а вот
    /// арифметика на i64::MAX ломается — см. историю ниже.
    pub const MAX_CENTS: i64 = 1_000_000_000;

    /// Единственный вход. Не пройдут ни отрицательное, ни абсурдно большое.
    ///
    /// Верхняя граница здесь не для красоты. Пока её не было, тип гарантировал
    /// только ЗНАК, и `discount` паниковал на `Money::try_new(i64::MAX)`:
    /// `c * 10` переполняло i64 («attempt to multiply with overflow»). То есть
    /// «неверное состояние непредставимо» держалось на честном слове, а не на
    /// типе. Теперь c <= 10^9, значит c*10 <= 10^10 — до i64::MAX (~9.2·10^18)
    /// далеко, и рассуждение про безопасность арифметики стало доказуемым.
    pub fn try_new(cents: i64) -> Result<Self, MoneyError> {
        if cents < 0 {
            return Err(MoneyError::Negative(cents));
        }
        if cents > Self::MAX_CENTS {
            return Err(MoneyError::TooLarge(cents));
        }
        Ok(Money(cents))
    }

    /// Значение в копейках.
    pub fn cents(self) -> i64 {
        self.0
    }

    /// Вычитание, которое не может уйти в минус: результат снова Money.
    ///
    /// Насыщение вместо паники — осознанный выбор домена: скидка не может
    /// превысить сумму, и если это вдруг случится, честнее отдать ноль, чем
    /// уронить процесс на проде.
    pub fn saturating_sub(self, other: Money) -> Money {
        Money((self.0 - other.0).max(0))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // Модуль #[cfg(test)] лежит РЯДОМ с кодом и видит приватное — в том числе
    // поле Money.0, до которого внешний тест из tests/ не дотянется.
    #[test]
    fn конструктор_единственный_вход() {
        assert_eq!(Money::try_new(0).unwrap().cents(), 0);
        assert_eq!(Money::try_new(1).unwrap().cents(), 1);
        assert_eq!(Money::try_new(-1), Err(MoneyError::Negative(-1)));
    }

    // Регрессия. Без верхней границы discount(Money::try_new(i64::MAX)) паниковал
    // на `c * 10` — переполнение i64. Тип гарантировал знак, но не диапазон,
    // и «арифметика безопасна по построению» было фигурой речи.
    #[test]
    fn верхняя_граница_закрывает_переполнение() {
        assert_eq!(
            Money::try_new(i64::MAX),
            Err(MoneyError::TooLarge(i64::MAX))
        );
        assert_eq!(
            Money::try_new(Money::MAX_CENTS + 1),
            Err(MoneyError::TooLarge(Money::MAX_CENTS + 1))
        );
        // на самом максимуме арифметика скидки уже не переполняется
        let max = Money::try_new(Money::MAX_CENTS).unwrap();
        assert_eq!(
            crate::pricing::discount(max).cents(),
            Money::MAX_CENTS * 10 / 100
        );
    }

    #[test]
    fn видим_приватное_поле() {
        // Внутри модуля это законно. Из tests/ — ошибка компиляции.
        let m = Money::try_new(500).unwrap();
        assert_eq!(m.0, 500);
    }

    #[test]
    fn вычитание_не_уходит_в_минус() {
        let a = Money::try_new(100).unwrap();
        let b = Money::try_new(300).unwrap();
        assert_eq!(a.saturating_sub(b), Money::try_new(0).unwrap());
    }
}
