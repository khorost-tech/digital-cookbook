//! ПРИЁМ 2: интеграционные тесты в `tests/`.
//!
//! Файл в tests/ — отдельный крейт. Он подключает библиотеку так же, как это
//! сделает чужой код: только через `pub`. Это и есть смысл папки — проверять
//! крейт снаружи, глазами потребителя.
//!
//! Разница с `#[cfg(test)] mod tests` не стилистическая, а физическая:
//! оттуда видно приватное, отсюда — нет. Строка ниже не скомпилируется, и это
//! не баг, а граница:
//!
//!     let m = Money::try_new(500).unwrap();
//!     assert_eq!(m.0, 500);   // error[E0616]: field `0` is private
//!
//! Что нельзя проверить отсюда — то и не является контрактом крейта.
//!
//!     cargo test --test black_box

use rust_testing::money::{Money, MoneyError};
use rust_testing::pricing::{discount, price};

fn m(cents: i64) -> Money {
    Money::try_new(cents).unwrap()
}

#[test]
fn публичный_контракт_скидки() {
    assert_eq!(discount(m(9_999)).cents(), 0);
    assert_eq!(discount(m(10_000)).cents(), 500);
    assert_eq!(discount(m(20_000)).cents(), 2_000);
}

#[test]
fn публичный_контракт_цены() {
    assert_eq!(price(m(10_000)).cents(), 9_500);
    assert_eq!(price(m(25_000)).cents(), 22_500);
}

#[test]
fn ошибка_конструктора_часть_контракта() {
    // Тип ошибки публичный — значит потребитель может на него полагаться,
    // и мы обязаны его не ломать.
    assert_eq!(Money::try_new(-1), Err(MoneyError::Negative(-1)));
}
