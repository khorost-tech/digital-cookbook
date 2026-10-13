"""Сценарий выполнения: тот же код, но запущенный, без проверки типов.

ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ — И ЧТО НЕТ. Проверяются ИНВАРИАНТЫ: итог не
отрицателен, скидка не делает заказ выплатой покупателю. Не проверяются
конкретные суммы.

Это не упрощение ради краткости, а суть одного из плеч. Утверждения об
инвариантах ловят нарушенное правило, но не ловят правдоподобно неверное
число: если из-за испорченных данных надбавка за срочность применилась
там, где не должна была, итог останется положительным и все проверки
пройдут. Такой дефект виден только на границе, где данные разбираются.

Данные читаются БЕЗ pydantic — плечо «голый рантайм» именно про это:
разобрали json, построили объекты, посчитали.
"""

import json
import sys
from decimal import Decimal
from pathlib import Path

import orders

DATA = Path(__file__).with_name("data.json")


def load(path: Path = DATA) -> list[orders.Order]:
    """Разбор без валидации: как это выглядит, когда границы нет."""
    raw = json.loads(path.read_text(encoding="utf-8"))
    out = []
    for item in raw:
        lines = [orders.Line(sku=line["sku"], quantity=line["quantity"],
                             price=Decimal(str(line["price"])))
                 for line in item["lines"]]
        discount = item.get("discount")
        out.append(orders.Order(
            id=item["id"],
            customer=item["customer"],
            lines=lines,
            express=item["express"],
            discount=None if discount is None else Decimal(str(discount)),
        ))
    return out


def main() -> int:
    failures = []
    for order in load():
        value = orders.total(order)
        if value < orders.ZERO:
            failures.append(
                f"заказ {order.id}: итог {value} отрицателен — купон "
                "крупнее корзины обязан гасить её в ноль")
    if failures:
        for line in failures:
            print("НАРУШЕН ИНВАРИАНТ: " + line, file=sys.stderr)
        return 1
    print(f"инварианты соблюдены на {len(load())} заказах")
    return 0


if __name__ == "__main__":
    sys.exit(main())
