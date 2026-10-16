"""Ленивая загрузка в async-сессии: что происходит на самом деле.

Это не замер, а фиксация ПОВЕДЕНИЯ — факт «да/нет», который не зависит ни
от машины, ни от разброса. Такие факты переживают перезамеры, в отличие от
процентов, и в статье они ценнее.

Проверяется ровно один вопрос: что будет, если в асинхронной сессии
обратиться к связи, которая не была загружена. Синхронная сессия в этот
момент молча сходит в базу — так и возникает N+1. Асинхронная сходить не
может: обращение к атрибуту синхронно по своей природе, а поход в базу
требует ожидания.

Записывается точный текст исключения и версия SQLAlchemy: поведение
версионное, и утверждение «SQLAlchemy бросает вот это» без версии
устаревает молча.

Отдельно проверяется, что то же самое обращение НЕ падает, если связь
загружена заранее. Без этой половины проверка доказывала бы лишь то, что
код в принципе умеет падать.
"""

import asyncio
import json
import os
import sys

from sqlalchemy import select
from sqlalchemy.ext.asyncio import async_sessionmaker, create_async_engine
from sqlalchemy.orm import selectinload

sys.path.insert(0, "/app")

import common  # noqa: E402
from models import Author  # noqa: E402


async def probe(preload):
    engine = create_async_engine(common.dsn_for("asyncpg"),
                                 pool_size=1, max_overflow=0)
    maker = async_sessionmaker(engine, expire_on_commit=False)
    stmt = select(Author).where(Author.id == 1)
    if preload:
        stmt = stmt.options(selectinload(Author.books))
    try:
        async with maker() as session:
            author = (await session.execute(stmt)).scalars().one()
            try:
                books = len(author.books)
            except Exception as exc:  # noqa: BLE001 — текст и есть результат
                return {"raised": True,
                        "type": type(exc).__name__,
                        "message": str(exc).splitlines()[0][:200]}
            return {"raised": False, "books": books}
    finally:
        await engine.dispose()


def main():
    out_path = os.environ.get("OUT", "/fixtures/lazy-async.txt")
    rows = []
    for preload in (False, True):
        row = asyncio.run(probe(preload))
        row["preloaded"] = preload
        row["sqlalchemy"] = common.versions()["sqlalchemy"]
        rows.append(row)
        print(json.dumps(row, ensure_ascii=False, sort_keys=True))

    with open(out_path, "a", encoding="utf-8") as out:
        for row in rows:
            out.write(json.dumps(row, ensure_ascii=False,
                                 sort_keys=True) + "\n")

    lazy, eager = rows
    if not lazy["raised"]:
        print("ПРОВЕРКА НЕ СОСТОЯЛАСЬ: обращение к незагруженной связи в "
              "async-сессии прошло без ошибки — либо связь всё же была "
              "загружена, либо поведение изменилось", file=sys.stderr)
        return 2
    if eager["raised"]:
        print("ПРОВЕРКА НЕ СОСТОЯЛАСЬ: падает и предзагруженная связь — "
              "значит падает не ленивая загрузка, а что-то другое",
              file=sys.stderr)
        return 3
    print()
    print(f"Незагруженная связь в async-сессии: {lazy['type']}")
    print(f"  {lazy['message']}")
    print(f"Предзагруженная той же сессией: {eager['books']} книг, без ошибки")
    print(f"SQLAlchemy {lazy['sqlalchemy']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
