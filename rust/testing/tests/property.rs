//! ПРИЁМ 4: property-based на proptest.
//!
//! Тот же сюжет и тот же домен, что в стенде architecture/testing/across-languages
//! (rapid в Go, jqwik в Java, Hypothesis в Python) — чтобы поведение proptest
//! можно было сравнить с ними напрямую.
//!
//!     cargo test --test property                    # инвариант, зелёный
//!     cargo test --test property --features prop_demo   # демо: наивный vs доменный
//!
//! ВАЖНО про замеры: proptest складывает найденные контрпримеры в
//! tests/*.proptest-regressions и переигрывает их первыми. Для честного замера
//! «нашёл ли заново» файл нужно удалять перед каждым прогоном.

use proptest::prelude::*;
use rust_testing::money::Money;
use rust_testing::pricing::price;
#[cfg(feature = "prop_demo")]
use rust_testing::pricing::{TIER1_CENTS, TIER2_CENTS};

fn m(cents: i64) -> Money {
    Money::try_new(cents).unwrap()
}

proptest! {
    /// Инвариант, который ДЕРЖИТСЯ: скидка не больше суммы, цена не отрицательна.
    ///
    /// Половина инварианта тут — не заслуга теста: «цена не отрицательна»
    /// гарантирует тип Money, её нельзя нарушить даже нарочно. Проверяем то,
    /// что типом не выражено: скидка не превышает сумму.
    #[test]
    fn скидка_не_больше_суммы(cents in 0i64..1_000_000_000) {
        let total = m(cents);
        let p = price(total);
        prop_assert!(p.cents() <= total.cents(),
            "цена {} больше суммы {}", p.cents(), total.cents());
    }
}

/// Само свойство: заказ на большую сумму не может стоить дешевле.
///
/// Звучит очевидно, но у тиров скачок: на 99.99 скидки нет (платим 99.99), на
/// 100.00 сразу 5% (платим 95.00). Заплатить больше выходит дешевле.
///
/// Это НЕ баг в коде: код точно соответствует спецификации, немонотонна сама
/// таблица тиров. Property-тест ищет дыру в постановке, а не отклонение от неё.
#[cfg(feature = "prop_demo")]
fn monotonic(a: i64, b: i64) -> Result<(), TestCaseError> {
    let (lo, hi) = if a <= b { (a, b) } else { (b, a) };
    prop_assert!(
        price(m(lo)).cents() <= price(m(hi)).cents(),
        "заказ на {} стоит {}, а на {} — {}: заплатить больше оказалось дешевле",
        lo,
        price(m(lo)).cents(),
        hi,
        price(m(hi)).cents()
    );
    Ok(())
}

#[cfg(feature = "prop_demo")]
proptest! {
    /// Наивный генератор: «просто дай числа из диапазона».
    /// Нарушение занимает узкую полосу входов — попадает туда редко.
    #[test]
    fn монотонность_наивный(a in 0i64..30_000, b in 0i64..30_000) {
        monotonic(a, b)?;
    }
}

#[cfg(feature = "prop_demo")]
proptest! {
    /// Генератор знает СТРУКТУРУ домена (у скидки есть пороги), но не знает про
    /// нарушение: ответ мы не подсказываем, находит его по-прежнему свойство.
    #[test]
    fn монотонность_доменный(
        a in prop_oneof![0i64..30_000, (TIER1_CENTS-500)..(TIER1_CENTS+500), (TIER2_CENTS-500)..(TIER2_CENTS+500)],
        b in prop_oneof![0i64..30_000, (TIER1_CENTS-500)..(TIER1_CENTS+500), (TIER2_CENTS-500)..(TIER2_CENTS+500)],
    ) {
        monotonic(a, b)?;
    }
}
