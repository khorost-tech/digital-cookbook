"""Тонкий слой над доменом — то, что тестируем дублёрами."""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Protocol

import pricing


@dataclass(frozen=True)
class Order:
    """Сохранённый заказ: total_cent — до скидки, price_cent — к оплате."""

    id: str
    user_id: str
    total_cent: int
    price_cent: int


class Store(Protocol):
    """Граница нашего кода: хранилище заказов."""

    def save(self, o: Order) -> None: ...

    def by_user(self, user_id: str) -> list[Order]: ...


class Notifier(Protocol):
    """Вторая граница: отправка уведомления."""

    def notify(self, user_id: str, text: str) -> None: ...


class OrderService:
    def __init__(self, store: Store, notifier: Notifier) -> None:
        self._store = store
        self._notifier = notifier

    def create(self, id: str, user_id: str, total_cents: int) -> Order:
        """Считает цену, сохраняет заказ, уведомляет пользователя.

        Переменная окружения DISCOUNT_BUG=1 включает намеренный дефект
        (скидка теряется) — им проверяем, кто из дублёров его ловит.
        """
        if total_cents < 0:
            raise ValueError(f"сумма заказа отрицательна: {total_cents}")

        if os.environ.get("DISCOUNT_BUG") == "1":
            p = total_cents  # БАГ: скидка потеряна
        else:
            p = pricing.price(total_cents)

        o = Order(id=id, user_id=user_id, total_cent=total_cents, price_cent=p)
        self._store.save(o)
        try:
            # Уведомление не критично: заказ уже сохранён.
            self._notifier.notify(user_id, f"Заказ {id} принят, к оплате {p} коп.")
        except Exception:  # noqa: BLE001 — намеренно проглатываем
            pass
        return o
