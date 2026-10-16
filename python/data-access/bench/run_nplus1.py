"""N+1 и стратегии загрузки связей: считаем запросы, а не время.

ПОЧЕМУ СЧЁТ ЗАПРОСОВ, А НЕ ВРЕМЯ. Время зависит от машины, от разогрева, от
соседей по хосту; число запросов — не зависит ни от чего. Это целое число,
одинаковое на ноутбуке и на сервере, и оно переживёт любой перезамер. В
волне 2 точные проценты не пережили трёх поколений фикстур, а качественные
утверждения выстояли — здесь метрика качественная по своей природе.

ПОЧЕМУ СЧИТАЕМ НА СТОРОНЕ БАЗЫ. Счётчик по событиям SQLAlchemy видит только
то, что SQLAlchemy сама и отправила. Всё, чего клиент не знает — неявный
BEGIN, повторные обращения драйвера, — мимо него пройдёт. pg_stat_statements
считает то, что база действительно выполнила.

Из счёта исключены служебные команды (BEGIN, COMMIT, SET, обнуление
статистики): они есть у всех стратегий одинаково и к N+1 отношения не
имеют. Считаются только обращения к таблицам стенда.

ТРИ СТРАТЕГИИ И ДВА УРОВНЯ ВЛОЖЕННОСТИ.

    lazy          загрузка по требованию, поведение SQLAlchemy по умолчанию
    selectinload  отдельный запрос на уровень
    joinedload    один запрос на всё, соединением

Двух уровней (автор -> книги -> отзывы) достаточно, чтобы отличить N+1 от
N+1+M и увидеть, чем joinedload платит за единственный запрос: строки
верхних уровней в его результате повторяются столько раз, сколько у них
потомков. Это не победа над N+1, а обмен.
"""

import json
import os
import sys
import time

import psycopg
from sqlalchemy import create_engine, select
from sqlalchemy.orm import joinedload, selectinload, sessionmaker

sys.path.insert(0, "/app")

import common  # noqa: E402
from models import Author, Book  # noqa: E402

AUTHORS = 20

# Только обращения к таблицам стенда. BEGIN, COMMIT и SET одинаковы у всех
# стратегий и к вопросу отношения не имеют.
COUNT_SQL = """
SELECT coalesce(sum(calls), 0), coalesce(sum(rows), 0)
FROM pg_stat_statements
WHERE query ~* '(authors|books|reviews)'
  AND query !~* '^(BEGIN|COMMIT|ROLLBACK|SET|SHOW)'
  AND query !~* 'pg_stat_statements'
"""


def stats(dsn):
    with psycopg.connect(dsn) as conn, conn.cursor() as cur:
        cur.execute(COUNT_SQL)
        calls, rows = cur.fetchone()
    return int(calls), int(rows)


def reset(dsn):
    with psycopg.connect(dsn) as conn, conn.cursor() as cur:
        cur.execute("SELECT pg_stat_statements_reset()")
        conn.commit()


def statement(strategy):
    base = select(Author).where(Author.id <= AUTHORS).order_by(Author.id)
    if strategy == "lazy":
        return base
    if strategy == "selectinload":
        return base.options(
            selectinload(Author.books).selectinload(Book.reviews))
    if strategy == "joinedload":
        return base.options(
            joinedload(Author.books).joinedload(Book.reviews))
    raise ValueError(strategy)


def touch(authors):
    """Обходит все связи. Именно обход и запускает ленивую загрузку.

    Возвращает контрольные суммы: без них «стратегия ничего не загрузила»
    выглядело бы как «стратегия обошлась одним запросом».
    """
    books = reviews = 0
    rating = 0
    for a in authors:
        for b in a.books:
            books += 1
            for r in b.reviews:
                reviews += 1
                rating += r.rating
    return {"books": books, "reviews": reviews, "rating_sum": rating}


def run_case(dsn, strategy, round_no):
    engine = create_engine(common.dsn_for("psycopg"),
                           pool_size=1, max_overflow=0)
    maker = sessionmaker(engine, expire_on_commit=False)
    try:
        # Прогрев вне счёта: первый запрос компилирует выражение и
        # наполняет кэши, и его стоимость к стратегии отношения не имеет.
        with maker() as session:
            touch(session.execute(statement(strategy)).scalars().unique().all())

        reset(dsn)
        started = time.perf_counter()
        with maker() as session:
            authors = session.execute(
                statement(strategy)).scalars().unique().all()
            checks = touch(authors)
        elapsed = time.perf_counter() - started
        calls, rows = stats(dsn)
    finally:
        engine.dispose()

    return {
        "scenario": "nplus1",
        "strategy": strategy,
        "round": round_no,
        "authors": AUTHORS,
        "queries": calls,
        "rows_from_server": rows,
        "ms": round(elapsed * 1000, 3),
        **checks,
    }


def main():
    dsn = os.environ["DSN"]
    rounds = int(os.environ.get("ROUNDS", "5"))
    out_path = os.environ.get("OUT", "/fixtures/nplus1.txt")

    with open(out_path, "a", encoding="utf-8") as out:
        for r in range(1, rounds + 1):
            for strategy in ("lazy", "selectinload", "joinedload"):
                row = run_case(dsn, strategy, r)
                out.write(json.dumps(row, ensure_ascii=False,
                                     sort_keys=True) + "\n")
                out.flush()
            print(f"круг {r} готов", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
