//! Тонкий слой над доменом — место для дублёров.
//!
//! Границы заданы трейтами: в Rust это и есть «порт». mockall умеет делать из
//! трейта мок автоматически, но фейк руками тут пишется в десяток строк — и
//! стенд показывает, когда что уместнее.

use crate::money::Money;
use crate::pricing;
use thiserror::Error;

#[derive(Debug, Error, PartialEq, Eq)]
pub enum ServiceError {
    #[error("сохранение заказа {0}: хранилище недоступно")]
    Store(String),
}

/// Сохранённый заказ: `total` — сумма до скидки, `price` — к оплате.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Order {
    pub id: String,
    pub user_id: String,
    pub total: Money,
    pub price: Money,
}

/// Граница: хранилище заказов.
#[cfg_attr(test, mockall::automock)]
pub trait Store {
    fn save(&mut self, order: &Order) -> Result<(), ServiceError>;
    fn by_user(&self, user_id: &str) -> Vec<Order>;
}

/// Вторая граница: уведомления.
#[cfg_attr(test, mockall::automock)]
pub trait Notifier {
    fn notify(&self, user_id: &str, text: &str);
}

pub struct OrderService<S: Store, N: Notifier> {
    pub store: S,
    pub notifier: N,
}

impl<S: Store, N: Notifier> OrderService<S, N> {
    /// Считает цену, сохраняет заказ, уведомляет пользователя.
    ///
    /// Обратите внимание, чего здесь НЕТ: проверки «сумма не отрицательна».
    /// Она невозможна — `Money` не построить отрицательной, и это гарантирует
    /// компилятор, а не тест.
    pub fn create(&mut self, id: &str, user_id: &str, total: Money) -> Result<Order, ServiceError> {
        let price = if cfg!(feature = "bug") {
            total // БАГ (только с --features bug): скидка потеряна
        } else {
            pricing::price(total)
        };

        let order = Order {
            id: id.to_string(),
            user_id: user_id.to_string(),
            total,
            price,
        };
        self.store.save(&order)?;
        // Уведомление не критично: заказ уже сохранён.
        self.notifier.notify(
            user_id,
            &format!("Заказ {id} принят, к оплате {} коп.", price.cents()),
        );
        Ok(order)
    }
}

// ПРИЁМ 5: дублёры — mockall против фейка руками.
//
//     cargo test service                  # зелено: дефекта нет
//     cargo test service --features bug   # демо: кто поймает
//
// Почему этот тест живёт ЗДЕСЬ, а не в tests/: `#[cfg_attr(test, automock)]`
// раскрывается только когда крейт собирают с cfg(test), то есть для юнитов.
// Интеграционный тест из tests/ подключает библиотеку как обычную зависимость —
// без cfg(test), — и никаких MockStore там просто нет:
//     error[E0432]: unresolved import `rust_testing::service::MockStore`
// Это не баг mockall, а следствие того, что tests/ — отдельный крейт. Моки
// трейтов — инструмент юнит-теста; хотите их снаружи — выносите генерацию под
// отдельную feature.
#[cfg(test)]
mod tests {
    use super::*;

    fn m(cents: i64) -> Money {
        Money::try_new(cents).unwrap()
    }

    // --- ФЕЙК: рабочая реализация в памяти, десяток строк ---

    #[derive(Default)]
    struct FakeStore {
        orders: Vec<Order>,
    }

    impl Store for FakeStore {
        fn save(&mut self, order: &Order) -> Result<(), ServiceError> {
            self.orders.push(order.clone());
            Ok(())
        }
        fn by_user(&self, user_id: &str) -> Vec<Order> {
            self.orders
                .iter()
                .filter(|o| o.user_id == user_id)
                .cloned()
                .collect()
        }
    }

    struct NopNotifier;
    impl Notifier for NopNotifier {
        fn notify(&self, _user_id: &str, _text: &str) {}
    }

    /// Тест на фейке говорит о РЕЗУЛЬТАТЕ: заказ сохранён и читается обратно с
    /// правильной ценой. Под `--features bug` падает — и правильно делает.
    #[test]
    fn на_фейке_заказ_сохранён_со_скидкой() {
        let mut svc = OrderService {
            store: FakeStore::default(),
            notifier: NopNotifier,
        };

        svc.create("o-1", "u-1", m(10_000)).unwrap();

        let got = svc.store.by_user("u-1");
        assert_eq!(got.len(), 1, "ожидали один заказ");
        assert_eq!(got[0].price.cents(), 9_500, "к оплате: 100.00 минус 5%");
    }

    /// СЛАБАЯ проверка вызовов: save позвали один раз. Утверждение осмысленное
    /// (не потеряли и не задублировали запись), но про цену не знает — под
    /// `--features bug` остаётся ЗЕЛЁНЫМ.
    #[test]
    fn на_моке_слабая_проверка_только_число_вызовов() {
        let mut store = MockStore::new();
        store.expect_save().times(1).returning(|_| Ok(()));
        let mut notifier = MockNotifier::new();
        notifier.expect_notify().returning(|_, _| ());

        let mut svc = OrderService { store, notifier };
        svc.create("o-1", "u-1", m(10_000)).unwrap();
    }

    /// СИЛЬНАЯ проверка вызовов: withf смотрит НА АРГУМЕНТ — и ловит тот же
    /// дефект, что и фейк. Значит «фейк умеет, мок не умеет» неверно:
    /// пропускает не мок, а слабое утверждение. Разница не в силе, а в
    /// предмете — фейк про результат, мок про вызов.
    #[test]
    fn на_моке_сильная_проверка_смотрим_аргумент() {
        let mut store = MockStore::new();
        store
            .expect_save()
            .withf(|o: &Order| o.price.cents() == 9_500)
            .times(1)
            .returning(|_| Ok(()));
        let mut notifier = MockNotifier::new();
        notifier.expect_notify().returning(|_, _| ());

        let mut svc = OrderService { store, notifier };
        svc.create("o-1", "u-1", m(10_000)).unwrap();
    }

    /// Ошибка хранилища эскалируется, уведомление при этом не шлётся.
    #[test]
    fn ошибка_хранилища_эскалируется() {
        let mut store = MockStore::new();
        store
            .expect_save()
            .returning(|_| Err(ServiceError::Store("o-1".into())));
        let mut notifier = MockNotifier::new();
        notifier.expect_notify().never();

        let mut svc = OrderService { store, notifier };
        let err = svc.create("o-1", "u-1", m(10_000)).unwrap_err();
        assert_eq!(err, ServiceError::Store("o-1".into()));
    }
}
