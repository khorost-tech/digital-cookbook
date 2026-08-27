"""ПРИЁМ 3: дублёры. Python-специфика: тут есть unittest.mock и monkeypatch —
подменить можно ЧТО УГОДНО без всяких интерфейсов. Мощно и опасно: соблазн
мокать внутренности вместо границ здесь сильнее, чем где-либо.

Проверить контраст: DISCOUNT_BUG=1 pytest — тест на фейке падает, на моке нет.
"""

from unittest.mock import MagicMock

from service import Order, OrderService


class FakeStore:
    """ФЕЙК: рабочая реализация в памяти."""

    def __init__(self) -> None:
        self.orders: list[Order] = []

    def save(self, o: Order) -> None:
        self.orders.append(o)

    def by_user(self, user_id: str) -> list[Order]:
        return [o for o in self.orders if o.user_id == user_id]


def test_на_фейке_заказ_сохранён_со_скидкой() -> None:
    store = FakeStore()
    svc = OrderService(store, MagicMock())

    svc.create("o-1", "u-1", 10_000)

    got = store.by_user("u-1")
    assert len(got) == 1, "ожидали 1 заказ"
    assert got[0].price_cent == 9_500, "к оплате: 100.00 минус 5%"


def test_на_моке_слабая_проверка_только_число_вызовов() -> None:
    """СЛАБАЯ проверка вызовов. Утверждение осмысленное, но про цену не знает:
    под DISCOUNT_BUG=1 тест остаётся зелёным, хотя клиент платит полную сумму.
    """
    store = MagicMock()
    svc = OrderService(store, MagicMock())

    svc.create("o-1", "u-1", 10_000)

    store.save.assert_called_once()


def test_на_моке_сильная_проверка_захват_аргумента() -> None:
    """СИЛЬНАЯ проверка вызовов: смотрим, ЧТО понесли в хранилище.
    Под DISCOUNT_BUG=1 падает — как и тест на фейке.

    Отсюда честный вывод: дело не в «фейк умеет, мок не умеет», а в том, ЧТО
    утверждает тест. Разница между ними — в предмете: фейк про результат,
    мок про вызов.
    """
    store = MagicMock()
    svc = OrderService(store, MagicMock())

    svc.create("o-1", "u-1", 10_000)

    store.save.assert_called_once()
    (saved,), _ = store.save.call_args
    assert saved.price_cent == 9_500, "в хранилище понесли цену: 100.00 минус 5%"


def test_на_стабе_ошибка_уведомления_не_роняет_заказ() -> None:
    """СТАБ: заготовленное поведение, без проверок."""
    store = FakeStore()
    notifier = MagicMock()
    notifier.notify.side_effect = RuntimeError("канал недоступен")

    svc = OrderService(store, notifier)
    svc.create("o-1", "u-1", 5_000)

    assert len(store.by_user("u-1")) == 1, "заказ должен быть сохранён"
