"""Единый контракт плеч: что спрашиваем и как представляем ответ.

ПОЧЕМУ ПЯТЬ ПЛЕЧ, А НЕ ЧЕТЫРЕ. Напрашивающийся набор — сырой драйвер, Core,
ORM async и ORM sync — смешивает две переменные сразу: синхронное плечо
пришлось бы вести на psycopg, потому что у asyncpg синхронного интерфейса
нет. Тогда разница «синхронный против асинхронного» оказалась бы
неотделима от разницы драйверов, и любой вывод из неё был бы о двух вещах
одновременно.

Поэтому плеч пять, и каждое сравнение меняет РОВНО ОДНО:

    raw-asyncpg  ->  core-asyncpg  ->  orm-asyncpg     слой, драйвер тот же
    orm-asyncpg  ->  orm-psycopg-async                 драйвер, слой тот же
    orm-psycopg-async -> orm-psycopg-sync              модель, всё остальное то же

ПЯТЬ ЗАПРОСОВ, А НЕ ОДИН. Цена слоя — это работа в процессе Python:
компиляция выражения, построение объектов, приведение типов. Поход в базу —
это сеть плюс работа сервера. На запросе, который база выполняет за
миллисекунду, разница слоёв утонет, и стенд выдаст «все одинаковы» —
правдоподобно и бесполезно.

    one-row        одна строка: цена слоя видна почти целиком
    many-rows      пять тысяч строк: цена слоя умножается, работа базы мала
    heavy-sql      работает база, наружу выходит агрегат: у ORM здесь нет
                   сущностей — агрегат не ложится на модель
    heavy-rows     ТОТ ЖЕ SQL, что у heavy-entities, но ORM отдаёт
                   отображения строк, а не объекты
    heavy-entities тот же SQL, и ORM строит из результата 516 объектов Book

ЧЕТВЁРТЫЙ И ПЯТЫЙ ЗАПРОСЫ ПОЯВИЛИСЬ ПОСЛЕ ДВУХ КРУГОВ РЕВЬЮ, и оба раза по
одной причине: доказательство было слабее вывода.

Сначала вывод «цена ORM исчезает, когда работает база» опирался на
heavy-sql. Но там ORM-плечи выполняют выражение Core и отдают mappings,
потому что агрегат не ложится на сущность. Материализации в этом столбце
нет вовсе, и измерено было другое: как вокруг скалярного агрегата тонут
накладные расходы сессии.

Тогда добавился heavy-entities — тяжёлый запрос, отдающий 516 книг. Он
показал, что цена ORM никуда не исчезает. Но сравнивать его с heavy-sql
как «тот же запрос, только с объектами» тоже нельзя: это РАЗНЫЕ запросы, с
разной группировкой и разным временем сервера (14,1 против 11,7 мс).

heavy-rows закрывает и это. Его SQL совпадает с heavy-entities ДОСЛОВНО,
сервер выполняет ровно ту же работу, и единственная разница — строит ли
ORM объекты или отдаёт отображения. Только эта пара и есть контролируемое
сравнение; heavy-sql остаётся как случай, где материализовать нечего.

РАВЕНСТВО ОТВЕТА ПРОВЕРЯЕТСЯ, А НЕ ПРЕДПОЛАГАЕТСЯ. Сериализация здесь одна
на всех, и она намеренно строгая. Numeric приезжает Decimal, дата — date,
timestamptz — datetime в поясе сессии; у разных драйверов это разные
объекты с разным str(). Плечо, которое отдаёт 12.3 вместо 12.30 или
местное время вместо UTC, вернуло НЕ ТО ЖЕ САМОЕ, и сравнивать его с
остальными нельзя.
"""

import datetime as dt
import decimal
import hashlib
import json
import os

DSN = os.environ.get("DSN", "postgresql://stand:stand@db:5432/stand")


def dsn_for(driver):
    """DSN в форме, которую понимает SQLAlchemy для конкретного драйвера."""
    tail = DSN.split("://", 1)[1]
    return f"postgresql+{driver}://{tail}"


HEAVY_ROWS_SQL = (
    "SELECT id, author_id, title, published, price, pages FROM books "
    "WHERE id IN (SELECT book_id FROM reviews GROUP BY book_id "
    "HAVING avg(rating) >= 3.6) "
    "ORDER BY id"
)

# Один и тот же смысл запроса для всех плеч. Сырое плечо выполняет этот
# текст дословно; Core и ORM строят выражение сами — в этом и состоит их
# работа, за которую мы платим и которую измеряем.
SQL = {
    "one-row": (
        "SELECT id, author_id, title, published, price, pages "
        "FROM books WHERE id = 4242"
    ),
    "many-rows": (
        "SELECT id, author_id, title, published, price, pages "
        "FROM books ORDER BY id"
    ),
    # Работает база: соединение с пятьюдесятью тысячами отзывов,
    # группировка и сортировка по pages, по которому индекса нет намеренно.
    # Наружу выходит дюжина строк — материализовать почти нечего.
    "heavy-sql": (
        "SELECT b.pages / 100 AS bucket, "
        "count(*) AS reviews, "
        "round(avg(r.rating), 4) AS avg_rating "
        "FROM reviews r JOIN books b ON b.id = r.book_id "
        "GROUP BY b.pages / 100 "
        "ORDER BY b.pages / 100"
    ),
    # Тяжёлая агрегация, отдающая наружу строки книг. Порог 3.6 подобран
    # так, чтобы вернулось 516 книг: достаточно, чтобы материализация была
    # видна, и мало, чтобы заглушить работу сервера.
    #
    # ДВА ИМЕНИ ДЛЯ ОДНОГО SQL — не дубликат. heavy-rows и heavy-entities
    # выполняют дословно одинаковый запрос, и различаются только тем, что
    # ORM-плечи делают с результатом: отображения строк или объекты Book.
    # Разница между этими двумя столбцами и есть цена материализации,
    # очищенная от всего остального.
    "heavy-rows": HEAVY_ROWS_SQL,
    "heavy-entities": HEAVY_ROWS_SQL,
}

# Порядок и имена полей результата. Плечо обязано отдать ровно эти ключи в
# этом порядке: расхождение в наборе полей — тоже неравенство работы.
FIELDS = {
    "one-row": ("id", "author_id", "title", "published", "price", "pages"),
    "many-rows": ("id", "author_id", "title", "published", "price", "pages"),
    "heavy-sql": ("bucket", "reviews", "avg_rating"),
    "heavy-rows": ("id", "author_id", "title", "published", "price", "pages"),
    "heavy-entities": ("id", "author_id", "title", "published", "price",
                       "pages"),
}


def canon(value):
    """Каноническое представление одного значения.

    Здесь собраны ровно те типы, на которых плечи расходятся молча.
    """
    if isinstance(value, decimal.Decimal):
        # Quantize, а не str: asyncpg и psycopg отдают Decimal с разным
        # числом значащих нулей на одном и том же numeric, и без
        # приведения 12.30 и 12.3 дали бы разные хеши при равных данных.
        return f"{value.quantize(decimal.Decimal('0.0001')):f}"
    if isinstance(value, dt.datetime):
        # Всегда UTC и всегда с явным смещением: пояс сессии не одинаков
        # у драйверов по умолчанию.
        return value.astimezone(dt.timezone.utc).isoformat()
    if isinstance(value, dt.date):
        return value.isoformat()
    if isinstance(value, (int, str)) or value is None:
        return value
    if isinstance(value, float):
        # Float в результате быть не должно: numeric обязан приезжать
        # Decimal. Если он всё же float, драйвер потерял точность, и это
        # не то же самое значение.
        raise TypeError("в результате оказался float — точность потеряна "
                        "драйвером, плечо вернуло не то же самое")
    raise TypeError(f"неизвестный тип в результате: {type(value)!r}")


def serialize(query, rows):
    """Канонический JSON результата. Одна функция на все плечи."""
    fields = FIELDS[query]
    out = [{f: canon(row[f]) for f in fields} for row in rows]
    return json.dumps(out, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":"))


def fingerprint(query, rows):
    payload = serialize(query, rows)
    return {
        "rows": len(rows),
        "sha256": hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16],
    }


def versions():
    from importlib.metadata import version
    return {p: version(p) for p in
            ("sqlalchemy", "asyncpg", "psycopg", "alembic")}
