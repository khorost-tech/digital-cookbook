"""Подопытное приложение: одно на все стратегии сборки.

ЗАЧЕМ ОНО ВООБЩЕ ЕСТЬ. Сравнивать образы по размеру просто; сравнивать
их честно — нет. Образ можно ужать, выкинув то, без чего приложение
работать перестанет, и на весах это выглядит как победа. Поэтому у
каждого образа спрашивают одно и то же и сверяют ответ: пока ответы
совпадают, разница в размере — это разница в упаковке, а не в
содержимом.

ЧТО В ОТВЕТЕ. Результат вычисления, которому нужны реальные зависимости
(pydantic проверяет, SQLAlchemy строит запрос, orjson сериализует), и
версии этих зависимостей. Версии — тот же приём, что и хеш набора в
сценарии установки: «легче» никогда не должно означать «поставлено
меньше».

ЧЕГО В ОТВЕТЕ НЕТ. Ничего об окружении: ни версии интерпретатора, ни
платформы, ни путей. Они по строению разные — на то и стратегии, — и,
попав в ответ, обрушили бы проверку равенства на различии, которое как
раз ожидаемо.
"""

from importlib.metadata import version

import orjson
import structlog
from fastapi import FastAPI
from fastapi.responses import Response
from pydantic import BaseModel, Field
from sqlalchemy import Column, Integer, MetaData, String, Table, select

RUNTIME = ("fastapi", "uvicorn", "pydantic", "orjson", "sqlalchemy",
           "structlog", "httpx", "starlette", "anyio")

meta = MetaData()
books = Table(
    "books", meta,
    Column("id", Integer, primary_key=True),
    Column("title", String),
    Column("pages", Integer),
)


class Book(BaseModel):
    id: int
    title: str = Field(min_length=1)
    pages: int = Field(ge=1)


SEED = [
    Book(id=1, title="Полдень, XXI век", pages=320),
    Book(id=2, title="Улитка на склоне", pages=240),
    Book(id=3, title="Пикник на обочине", pages=224),
]

app = FastAPI()
log = structlog.get_logger()


def payload():
    """Одно и то же на любой стратегии сборки — иначе сравнение пустое."""
    stmt = (select(books.c.id, books.c.title)
            .where(books.c.pages > 230)
            .order_by(books.c.id))
    return {
        "query": " ".join(str(stmt).split()),
        "books": [b.model_dump() for b in SEED],
        "total_pages": sum(b.pages for b in SEED),
        "packages": {name: version(name) for name in RUNTIME},
    }


@app.get("/answer")
def answer():
    # orjson, а не встроенный json: во-первых, зависимость обязана быть
    # задействована, иначе её отсутствие в образе никто не заметит;
    # во-вторых, sort_keys даёт побайтово одинаковый ответ.
    body = orjson.dumps(payload(), option=orjson.OPT_SORT_KEYS)
    return Response(content=body, media_type="application/json")


@app.get("/health")
def health():
    return {"ok": True}
