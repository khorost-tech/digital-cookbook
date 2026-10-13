"""Граница: разбор входных данных через pydantic.

ЗАЧЕМ ОТДЕЛЬНЫЙ РУБЕЖ. Статический анализатор проверяет КОД, а на границе
приходят ДАННЫЕ, и про них он не знает ничего: поле объявлено `bool`,
аннотация верна, а в JSON лежит строка. Проверка типов при этом зелёная.

СТРОГОСТЬ ЗДЕСЬ НЕ СПЛОШНАЯ, И ЭТО НЕ КОМПРОМИСС РАДИ УДОБСТВА. Она
включена там, где тип в JSON однозначен, и выключена там, где его нет.

    строго    int, str, bool — у JSON есть ровно эти типы, и подмена
              одного другим означает, что отправитель прислал не то
    нестрого  Decimal — десятичного типа в JSON НЕТ. Деньги передают
              строкой именно потому, что число с плавающей точкой их
              портит; требовать здесь экземпляр Decimal значило бы
              требовать невозможного

Первая версия включала строгость сплошь и отвергала «240.00» — то есть
браковала корректные данные. Контроль это поймал сразу: на исправном
наборе плечо обязано быть зелёным, а оно краснело.

Что осталось строгим — существенно. По умолчанию pydantic приводит типы, и
строку «false» он превратит в булево False с полным удовольствием.
Приведение уместно, когда данные пришли из HTML-формы; на границе между
службами оно означает, что отправитель может прислать что угодно похожее,
а получатель молча догадается.
"""

from decimal import Decimal

from pydantic import BaseModel, ConfigDict, Field

import orders

# Деньги приходят строкой: у JSON нет десятичного типа.
Money = Field(strict=False)


class LineIn(BaseModel):
    model_config = ConfigDict(strict=True)

    sku: str
    quantity: int
    price: Decimal = Money


class OrderIn(BaseModel):
    model_config = ConfigDict(strict=True)

    id: int
    customer: str
    lines: list[LineIn]
    express: bool
    discount: Decimal | None = Field(default=None, strict=False)

    def to_domain(self) -> orders.Order:
        return orders.Order(
            id=self.id,
            customer=self.customer,
            lines=[orders.Line(sku=line.sku, quantity=line.quantity,
                               price=line.price) for line in self.lines],
            express=self.express,
            discount=self.discount,
        )
