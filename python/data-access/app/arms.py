"""Пять плеч доступа к данным. Один запрос — один и тот же результат.

Каждое плечо получает соединение, выполняет запрос и отдаёт список
словарей с полями из `common.FIELDS`. Ни одно плечо не сериализует
результат само: сериализация одна на всех, иначе сравнивалось бы
форматирование, а не доступ.

ПОЧЕМУ ПЯТЬ ПЛЕЧ. Напрашивающийся набор — сырой драйвер, Core, ORM async и
ORM sync — смешивает две переменные: синхронное плечо пришлось бы вести на
psycopg, потому что у asyncpg синхронного интерфейса нет, и разница
«синхронный против асинхронного» стала бы неотделима от разницы драйверов.
Здесь каждое сравнение меняет ровно одно:

    raw-asyncpg  ->  core-asyncpg  ->  orm-asyncpg     слой, драйвер тот же
    orm-asyncpg  ->  orm-psycopg-async                 драйвер, слой тот же
    orm-psycopg-async -> orm-psycopg-sync              модель, всё прочее то же

ЖИЗНЕННЫЙ ЦИКЛ ОТДЕЛЁН ОТ ИТЕРАЦИИ. Соединение поднимается один раз на всю
пачку. Первая версия открывала его на каждой итерации, и замер выдал 23 мс
на запрос одной строки при 0,015 мс работы сервера: мерилась установка
соединения и создание цикла событий, а не доступ к данным. Числа при этом
выглядели совершенно правдоподобно — поймала их не проверка, а
несовместимость с физикой.

Пояс сессии задаётся один раз при подключении, а не перед каждым запросом:
`SET TIME ZONE` — это ещё один поход к серверу, и в цене слоя ему не место.
"""

import asyncio

import asyncpg
from sqlalchemy import (create_engine, event, func, literal_column, select,
                        text)
from sqlalchemy.ext.asyncio import async_sessionmaker, create_async_engine
from sqlalchemy.orm import sessionmaker

import common
from models import Book, books_t, reviews_t


# --------------------------------------------------------------------------
# Выражения Core и ORM. Строятся один раз: компиляция кэшируется самой
# SQLAlchemy, и пересоздавать выражение на каждой итерации значило бы мерить
# работу пользователя, а не библиотеки.
# --------------------------------------------------------------------------

CORE_STMT = {
    "one-row": select(books_t.c.id, books_t.c.author_id, books_t.c.title,
                      books_t.c.published, books_t.c.price, books_t.c.pages)
    .where(books_t.c.id == 4242),
    "many-rows": select(books_t.c.id, books_t.c.author_id, books_t.c.title,
                        books_t.c.published, books_t.c.price, books_t.c.pages)
    .order_by(books_t.c.id),
    "heavy-sql": None,  # заполняется ниже
}

# Деление написано через op("/"), и это не придирка к стилю. Три способа
# поделить на сто дают в SQLAlchemy 2.0 три РАЗНЫХ SQL:
#
#     pages / 100          ->  books.pages / CAST(100 AS NUMERIC)
#     pages // 100         ->  books.pages / %(pages_1)s
#     pages.op("/")(100)   ->  books.pages / 100
#
# Первый — истинное деление: в Python 3 оператор `/` возвращает дробь, и
# SQLAlchemy честно переносит эту семантику в SQL. В сыром же SQL
# `b.pages / 100` на целых операндах — деление ЦЕЛОЧИСЛЕННОЕ. То есть плечи
# считали разные величины при одинаковом на вид коде.
#
# Второй делит целочисленно, но подставляет связанный параметр, а в SELECT и
# GROUP BY параметры выходят РАЗНЫЕ, и PostgreSQL отказывается признавать
# выражения одинаковыми. Именно этот отказ расхождение и вскрыл — громко.
# Повезло: будь обе стороны согласованно неправы, в фикстуру уехали бы
# разные величины под одним именем.
_bucket = books_t.c.pages.op("/")(literal_column("100"))

CORE_STMT["heavy-sql"] = (
    select(
        _bucket.label("bucket"),
        func.count().label("reviews"),
        func.round(func.avg(reviews_t.c.rating), literal_column("4"))
        .label("avg_rating"),
    )
    .select_from(reviews_t.join(books_t, books_t.c.id == reviews_t.c.book_id))
    .group_by(_bucket)
    .order_by(_bucket)
)

# Подзапрос отбирает книги со средней оценкой не ниже 3.6: агрегация идёт
# по всем пятидесяти тысячам отзывов, а наружу выходит 516 книг.
_well_rated = (
    select(reviews_t.c.book_id)
    .group_by(reviews_t.c.book_id)
    .having(func.avg(reviews_t.c.rating) >= literal_column("3.6"))
)

_heavy_rows_core = (
    select(books_t.c.id, books_t.c.author_id, books_t.c.title,
           books_t.c.published, books_t.c.price, books_t.c.pages)
    .where(books_t.c.id.in_(_well_rated))
    .order_by(books_t.c.id)
)
# Одно выражение под двумя именами: у Core и сырого драйвера heavy-rows и
# heavy-entities неразличимы по построению, и это само по себе контроль —
# их числа обязаны совпасть.
CORE_STMT["heavy-rows"] = _heavy_rows_core
CORE_STMT["heavy-entities"] = _heavy_rows_core

ORM_STMT = {
    "one-row": select(Book).where(Book.id == 4242),
    "many-rows": select(Book).order_by(Book.id),
    # heavy-sql у ORM-плеча идёт тем же выражением, что у Core: агрегат не
    # ложится на сущность, и притворяться, что ложится, было бы подгонкой.
    # Поэтому цена ORM на этом запросе обязана совпасть с Core.
    #
    # ВАЖНО, ЧТО ИЗ ЭТОГО СЛЕДУЕТ, А ЧТО НЕТ. Совпадение здесь означает: у
    # скалярного агрегата тонут накладные расходы сессии и драйвера. Оно НЕ
    # означает, что исчезает цена материализации: материализовать тут
    # нечего. Ради этого различия и заведён heavy-entities — там сервер
    # работает столько же, но ORM строит настоящие объекты.
    "heavy-sql": CORE_STMT["heavy-sql"],
    # heavy-rows идёт выражением Core: ORM выполняет его и отдаёт
    # отображения строк, не строя объектов.
    "heavy-rows": _heavy_rows_core,
    # heavy-entities — ТОТ ЖЕ SQL, но через сущность: SQLAlchemy строит из
    # результата объекты Book. Разница между этими двумя столбцами и есть
    # цена материализации при полностью одинаковой работе сервера.
    "heavy-entities": (
        select(Book)
        .where(Book.id.in_(_well_rated))
        .order_by(Book.id)
    ),
}

# Запросы, на которых ORM-плечи НЕ строят сущностей.
NO_ENTITIES = ("heavy-sql", "heavy-rows")

UTC = "SET TIME ZONE 'UTC'"


def _book_rows(objs):
    return [{"id": o.id, "author_id": o.author_id, "title": o.title,
             "published": o.published, "price": o.price, "pages": o.pages}
            for o in objs]


def _mapping_rows(result, query):
    fields = common.FIELDS[query]
    return [{f: m[f] for f in fields} for m in result.mappings()]


def _utc_on_connect(engine):
    """Пояс задаётся один раз на физическое соединение, а не перед запросом.

    Через событие драйвера, а не через session.execute: последнее — лишний
    поход к серверу в каждой итерации, и он попал бы в цену слоя.
    """
    target = getattr(engine, "sync_engine", engine)

    @event.listens_for(target, "connect")
    def _set_tz(dbapi_conn, _record):  # noqa: ANN001
        cur = dbapi_conn.cursor()
        try:
            cur.execute(UTC)
        finally:
            cur.close()


# --------------------------------------------------------------------------
# raw-asyncpg — пол, ниже которого доступа к данным из Python не бывает
# --------------------------------------------------------------------------

class RawAsyncpg:
    name = "raw-asyncpg"
    is_async = True

    async def setup(self):
        self.conn = await asyncpg.connect(common.DSN)
        await self.conn.execute(UTC)

    async def run(self, query):
        rows = await self.conn.fetch(common.SQL[query])
        return [dict(r) for r in rows]

    async def teardown(self):
        await self.conn.close()


# --------------------------------------------------------------------------
# SQLAlchemy Core на том же asyncpg — прибавка компиляции выражения
# --------------------------------------------------------------------------

class CoreAsyncpg:
    name = "core-asyncpg"
    is_async = True
    driver = "asyncpg"

    async def setup(self):
        self.engine = create_async_engine(
            common.dsn_for(self.driver), pool_size=1, max_overflow=0)
        _utc_on_connect(self.engine)
        self.conn = await self.engine.connect()

    async def run(self, query):
        result = await self.conn.execute(CORE_STMT[query])
        return _mapping_rows(result, query)

    async def teardown(self):
        await self.conn.close()
        await self.engine.dispose()


# --------------------------------------------------------------------------
# ORM на asyncpg — прибавка построения объектов и identity map
# --------------------------------------------------------------------------

class OrmAsyncpg:
    name = "orm-asyncpg"
    is_async = True
    driver = "asyncpg"

    async def setup(self):
        self.engine = create_async_engine(
            common.dsn_for(self.driver), pool_size=1, max_overflow=0)
        _utc_on_connect(self.engine)
        self.maker = async_sessionmaker(self.engine, expire_on_commit=False)
        # Физическое соединение поднимается здесь, а не внутри первой
        # итерации: иначе первая итерация стоила бы на порядок дороже
        # остальных и утащила бы за собой среднее.
        async with self.engine.connect() as conn:
            await conn.execute(text("SELECT 1"))

    async def run(self, query):
        # Сессия на операцию — обычный способ работы с ORM, и её создание
        # входит в цену слоя честно: физическое соединение при этом берётся
        # из пула, а не открывается заново.
        async with self.maker() as session:
            result = await session.execute(ORM_STMT[query])
            if query in NO_ENTITIES:
                return _mapping_rows(result, query)
            return _book_rows(result.scalars().all())

    async def teardown(self):
        await self.engine.dispose()


class OrmPsycopgAsync(OrmAsyncpg):
    """То же самое, но другой драйвер. Изолирует цену драйвера."""
    name = "orm-psycopg-async"
    driver = "psycopg"


# --------------------------------------------------------------------------
# ORM синхронный на том же psycopg — изолирует цену асинхронной модели
# --------------------------------------------------------------------------

class OrmPsycopgSync:
    name = "orm-psycopg-sync"
    is_async = False

    def setup(self):
        self.engine = create_engine(
            common.dsn_for("psycopg"), pool_size=1, max_overflow=0)
        _utc_on_connect(self.engine)
        self.maker = sessionmaker(self.engine, expire_on_commit=False)
        with self.engine.connect() as conn:
            conn.execute(text("SELECT 1"))

    def run(self, query):
        with self.maker() as session:
            result = session.execute(ORM_STMT[query])
            if query in NO_ENTITIES:
                return _mapping_rows(result, query)
            return _book_rows(result.scalars().all())

    def teardown(self):
        self.engine.dispose()


ARMS = (RawAsyncpg, CoreAsyncpg, OrmAsyncpg, OrmPsycopgAsync, OrmPsycopgSync)
BY_NAME = {cls.name: cls for cls in ARMS}


def execute(arm, query):
    """Один прогон с поднятием и опусканием плеча.

    Годится для проверки равенства, где важен результат, и НЕ годится для
    замера: установка соединения тут дороже самого запроса на два порядка.
    Замер ведёт runner.py, который поднимает плечо один раз на всю пачку.
    """
    if arm.is_async:
        return asyncio.run(_async_once(arm, query))
    arm.setup()
    try:
        return arm.run(query)
    finally:
        arm.teardown()


async def _async_once(arm, query):
    await arm.setup()
    try:
        return await arm.run(query)
    finally:
        await arm.teardown()
