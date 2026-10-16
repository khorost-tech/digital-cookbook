"""Модели SQLAlchemy 2.0 и таблицы Core — общие для всех плеч на них.

Стиль 2.0 с типизированными атрибутами (`Mapped`, `mapped_column`) выбран
намеренно: именно он рекомендован и именно он даёт mypy что проверять. В
статье это будет отдельный сюжет — типизированные модели против словарей,
которыми отдаёт сырой драйвер.

Связи объявлены с `lazy="select"`, то есть ленивыми ПО УМОЛЧАНИЮ. Это не
недосмотр: сценарий N+1 обязан воспроизводить поведение по умолчанию, а не
подстроенное под ответ. Стратегии `selectinload` и `joinedload`
запрашиваются явно в самих запросах.
"""

import datetime as dt
import decimal

from sqlalchemy import (Column, Date, ForeignKey, Integer, MetaData, Numeric,
                        SmallInteger, Table, Text, DateTime)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, relationship


class Base(DeclarativeBase):
    pass


class Author(Base):
    __tablename__ = "authors"

    id: Mapped[int] = mapped_column(primary_key=True)
    name: Mapped[str]
    country: Mapped[str]
    born_at: Mapped[dt.date] = mapped_column(Date)

    books: Mapped[list["Book"]] = relationship(back_populates="author")


class Book(Base):
    __tablename__ = "books"

    id: Mapped[int] = mapped_column(primary_key=True)
    author_id: Mapped[int] = mapped_column(ForeignKey("authors.id"))
    title: Mapped[str]
    published: Mapped[dt.date] = mapped_column(Date)
    price: Mapped[decimal.Decimal] = mapped_column(Numeric(8, 2))
    pages: Mapped[int]

    author: Mapped["Author"] = relationship(back_populates="books")
    reviews: Mapped[list["Review"]] = relationship(back_populates="book")


class Review(Base):
    __tablename__ = "reviews"

    id: Mapped[int] = mapped_column(primary_key=True)
    book_id: Mapped[int] = mapped_column(ForeignKey("books.id"))
    rating: Mapped[int] = mapped_column(SmallInteger)
    body: Mapped[str]
    created_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True))

    book: Mapped["Book"] = relationship(back_populates="reviews")


# Отдельное описание тех же таблиц для Core. Дублирование намеренное: Core
# работает с Table, а не с моделями, и плечо на нём должно быть настоящим
# плечом Core, а не ORM, у которого отняли сессию.
core_metadata = MetaData()

books_t = Table(
    "books", core_metadata,
    Column("id", Integer, primary_key=True),
    Column("author_id", Integer, ForeignKey("authors.id")),
    Column("title", Text),
    Column("published", Date),
    Column("price", Numeric(8, 2)),
    Column("pages", Integer),
)

reviews_t = Table(
    "reviews", core_metadata,
    Column("id", Integer, primary_key=True),
    Column("book_id", Integer, ForeignKey("books.id")),
    Column("rating", SmallInteger),
    Column("body", Text),
    Column("created_at", DateTime(timezone=True)),
)
