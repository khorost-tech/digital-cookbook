"""Подопытный модуль: одно правило расчёта заказа.

Здесь лежит ПРАВИЛЬНАЯ реализация. Дефекты не живут в этом файле и не
переключаются флагом: статические анализаторы читают исходник, и ветка
`if os.environ[...]` не помогла бы — mypy увидел бы сразу все варианты и
пожаловался на все. Поэтому дефект — точечная правка исходника, она
описана в defects.py и применяется к копии перед прогоном.

Следствие важное: для каждого состояния кода все пять проверяющих видят
один и тот же файл, побайтово.
"""

from dataclasses import dataclass
from decimal import Decimal

# Надбавка за срочную доставку. Константа, а не поле заказа: так проще
# показать, что дефект external-data меняет ПОВЕДЕНИЕ, не ломая типов.
EXPRESS_SURCHARGE = Decimal("4.90")
ZERO = Decimal("0")


@dataclass(frozen=True)
class Line:
    sku: str
    quantity: int
    price: Decimal


@dataclass(frozen=True)
class Order:
    id: int
    customer: str
    lines: list[Line]
    express: bool
    # Скидки может не быть — и это не то же самое, что скидка ноль.
    discount: Decimal | None


def line_total(line: Line) -> Decimal:
    return line.price * line.quantity


def subtotal(order: Order) -> Decimal:
    return sum((line_total(line) for line in order.lines), ZERO)


def describe(order: Order) -> str:
    """Строка для журнала.

    В сценарии выполнения не вызывается — намеренно. На ней показано, что
    статический анализ видит код, до которого тесты не доходят.
    """
    return f"заказ {order.id}: строк {len(order.lines)}"


def total(order: Order) -> Decimal:
    """ПРАВИЛО: сумма строк, плюс надбавка за срочность, минус скидка.

    Итог не бывает отрицательным: купон крупнее корзины гасит её в ноль, а
    не превращает заказ в выплату покупателю.
    """
    value = subtotal(order)
    if order.express:
        value += EXPRESS_SURCHARGE
    if order.discount is not None:
        value -= order.discount
    return max(ZERO, value)
