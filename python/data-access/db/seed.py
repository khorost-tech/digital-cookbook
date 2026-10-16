"""Детерминированный посев и контрольная сумма набора данных.

ЗАЧЕМ КОНТРОЛЬНАЯ СУММА. Плечи сравниваются между собой, и сравнение имеет
смысл, только пока они работают с одним и тем же набором. Набор может
разъехаться незаметно: пересев с другим зерном, случайно оставленная
транзакция от прошлого сценария, миграция из сценария Alembic, которая
добавила столбец и не откатилась. Числа при этом останутся правдоподобными.

Поэтому посев печатает хеш содержимого, preflight его сверяет, и замер не
запускается на изменившемся наборе.

Хеш считается по СОДЕРЖИМОМУ в детерминированном порядке, а не по
count(*): подмена значения при том же числе строк — ровно тот случай,
который счётчик строк не увидит.
"""

import argparse
import datetime as dt
import hashlib
import json
import os
import random
import sys

AUTHORS = 500
BOOKS = 5_000
REVIEWS = 50_000
SEED = 20260824

COUNTRIES = ("Россия", "Аргентина", "Япония", "Норвегия", "Чили", "Индия")
WORDS = ("тихий", "северный", "стеклянный", "долгий", "медный", "полдень",
         "рубеж", "перегон", "радиус", "смотритель", "равновесие", "маяк")


def rows():
    """Возвращает три списка кортежей. Порядок и значения детерминированы."""
    rnd = random.Random(SEED)
    epoch = dt.date(1900, 1, 1)

    authors = []
    for i in range(1, AUTHORS + 1):
        authors.append((
            i,
            f"{WORDS[rnd.randrange(len(WORDS))].capitalize()} {i}",
            COUNTRIES[rnd.randrange(len(COUNTRIES))],
            epoch + dt.timedelta(days=rnd.randrange(30000)),
        ))

    books = []
    for i in range(1, BOOKS + 1):
        books.append((
            i,
            rnd.randrange(1, AUTHORS + 1),
            " ".join(rnd.choice(WORDS) for _ in range(3)),
            dt.date(1950, 1, 1) + dt.timedelta(days=rnd.randrange(27000)),
            # Цена в numeric: у Decimal и float разное представление, и
            # плечи обязаны сериализовать его одинаково. Тип выбран как
            # раз потому, что на нём эквивалентность легко нарушить.
            f"{rnd.randrange(100, 500000) / 100:.2f}",
            rnd.randrange(60, 1200),
        ))

    base = dt.datetime(2020, 1, 1, tzinfo=dt.timezone.utc)
    reviews = []
    for i in range(1, REVIEWS + 1):
        reviews.append((
            i,
            rnd.randrange(1, BOOKS + 1),
            rnd.randrange(1, 6),
            " ".join(rnd.choice(WORDS) for _ in range(rnd.randrange(4, 12))),
            base + dt.timedelta(seconds=rnd.randrange(150_000_000)),
        ))
    return authors, books, reviews


def digest(authors, books, reviews):
    h = hashlib.sha256()
    for name, data in (("authors", authors), ("books", books),
                       ("reviews", reviews)):
        h.update(name.encode())
        h.update(str(len(data)).encode())
        for row in data:
            # Каждое поле отдельно и с разделителем: склейка без
            # разделителя дала бы одинаковый хеш для ("ab","c") и ("a","bc").
            for field in row:
                h.update(str(field).encode("utf-8"))
                h.update(b"\x1f")
            h.update(b"\x1e")
    return h.hexdigest()


def db_rows(conn):
    """Читает содержимое базы в том же виде, в каком его строит генератор.

    Смысл в замыкании петли: хеш по сгенерированным данным проверяет только
    детерминированность генератора, а не то, ЧТО лежит в базе. Отличить
    «набор тот же» от «генератор тот же» можно, лишь прочитав базу.

    Часовой пояс задаётся явно. Без этого psycopg вернёт timestamptz в
    поясе сессии, его строковое представление разъедется с генераторским, и
    сверка будет падать на совершенно исправном наборе — то есть
    предохранитель начнёт кричать не о том.
    """
    with conn.cursor() as cur:
        cur.execute("SET TIME ZONE 'UTC'")
        cur.execute("SELECT id, name, country, born_at FROM authors ORDER BY id")
        authors = cur.fetchall()
        cur.execute("SELECT id, author_id, title, published, price, pages "
                    "FROM books ORDER BY id")
        books = [(i, a, t, p, f"{price:.2f}", pg)
                 for i, a, t, p, price, pg in cur.fetchall()]
        cur.execute("SELECT id, book_id, rating, body, created_at "
                    "FROM reviews ORDER BY id")
        reviews = cur.fetchall()
    return authors, books, reviews


def load(conn, authors, books, reviews):
    with conn.cursor() as cur:
        with open("/db/schema.sql", encoding="utf-8") as fh:
            cur.execute(fh.read())
        for table, cols, data in (
            ("authors", "(id, name, country, born_at)", authors),
            ("books", "(id, author_id, title, published, price, pages)", books),
            ("reviews", "(id, book_id, rating, body, created_at)", reviews),
        ):
            with cur.copy(f"COPY {table} {cols} FROM STDIN") as cp:
                for row in data:
                    cp.write_row(row)
        # Без ANALYZE планировщик работает по пустой статистике, и первый
        # сценарий получает другие планы, чем следующие. Это не мелочь:
        # сравнивались бы разные запросы под одним именем.
        cur.execute("ANALYZE")
    conn.commit()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true",
                    help="только напечатать ожидаемый хеш, не трогая базу")
    ap.add_argument("--verify", action="store_true",
                    help="сверить содержимое базы с ожидаемым хешем")
    args = ap.parse_args()

    authors, books, reviews = rows()
    expected = digest(authors, books, reviews)
    out = {"seed": SEED, "authors": len(authors), "books": len(books),
           "reviews": len(reviews), "fingerprint": expected}

    if args.verify:
        import psycopg
        with psycopg.connect(os.environ["DSN"]) as conn:
            got = digest(*db_rows(conn))
        out["actual"] = got
        out["match"] = got == expected
        print(json.dumps(out, ensure_ascii=False, sort_keys=True))
        if not out["match"]:
            print("SEED FAIL: содержимое базы не совпадает с ожидаемым — "
                  "набор данных изменился, замеры несравнимы",
                  file=sys.stderr)
            return 2
        return 0

    if not args.check:
        import psycopg
        with psycopg.connect(os.environ["DSN"]) as conn:
            load(conn, authors, books, reviews)

    print(json.dumps(out, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
