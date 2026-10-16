"""Автогенерация Alembic: что она видит и возвращает ли откат схему назад.

ДВА ВОПРОСА, И ВТОРОЙ ПОЧТИ НИКОГДА НЕ ПРОВЕРЯЮТ.

    увидела ли автогенерация изменение схемы
    вернул ли downgrade схему в исходное состояние

Первый обсуждают часто, второй — почти нет, хотя именно он выясняется в
худший момент: при откате неудачного релиза.

КАК ПРОВЕРЯЕТСЯ ОТКАТ. Не глазами и не по тексту миграции, а сравнением
снимков схемы до и после цикла upgrade -> downgrade. Текст миграции говорит
о намерении, снимок — о результате.

Снимок берётся из системного каталога, а НЕ через саму автогенерацию.
Спросить у Alembic «совпадает ли база с описанием» было бы кругом: он не
увидел бы ровно того, чего не видит по построению — например, оставшийся
server_default, — и объявил бы откат удачным. Проверять инструмент можно
только независимой от него меркой.

Снимок берётся и не через pg_dump: в образе стенда клиент версии 15, а
сервер 18, и pg_dump отказывается работать при таком расхождении. Системный
каталог от версии клиента не зависит вовсе.

ПОЧЕМУ БЕЗ ФАЙЛОВЫХ МИГРАЦИЙ. Здесь используется API автогенерации
напрямую: produce_migrations сравнивает базу с описанием и возвращает
список операций, а Operations их выполняет. Файловая обвязка ничего бы к
ответу не добавила, зато добавила бы шума — версии, ветки, зависимости.

КОНТРОЛЬНЫЙ СЛУЧАЙ ОБЯЗАТЕЛЕН И ИДЁТ ПЕРВЫМ. Автогенерация сравнивает базу
с ОПИСАНИЕМ ЦЕЛИКОМ. Если описание расходится с базой хоть в мелочи —
забытый индекс, не указанный NOT NULL, — она предложит операции, которых
никто не просил, и они подмешаются в каждый случай. Поэтому сперва
проверяется, что на НЕИЗМЕНЁННОМ описании операций ноль. Не ноль — разбор
дальше бессмыслен.

ДВА РЕЖИМА СРАВНЕНИЯ. Каждый случай прогоняется дважды: с умолчаниями и с
включёнными compare_type и compare_server_default. Разница между режимами и
есть ответ на вопрос «почему автогенерация этого не увидела».

Ожидание перед прогоном было такое: с умолчаниями Alembic не сравнивает ни
типы, ни server_default. Замер поправил его наполовину — на 1.19.1 смена
типа обнаруживается и с умолчаниями, а вот смена server_default у
существующего столбца не обнаруживается, пока сравнение не включено явно.
Ожидание здесь записано намеренно: важно, что числа его исправили, а не
подтвердили.
"""

import datetime as dt
import json
import os
import sys

from alembic.autogenerate import produce_migrations, render_python_code
from alembic.migration import MigrationContext
from alembic.operations import Operations
from sqlalchemy import (BigInteger, CheckConstraint, Column, Date, DateTime,
                        DefaultClause, ForeignKey, Index, Integer, MetaData,
                        Numeric, SmallInteger, String, Table, Text,
                        create_engine, text)

sys.path.insert(0, "/app")

import common  # noqa: E402


def baseline():
    """Описание, точно соответствующее db/schema.sql.

    Точность здесь не педантизм: любое расхождение всплывёт в контрольном
    случае как лишняя операция и обесценит все остальные.
    """
    md = MetaData()
    Table(
        "authors", md,
        Column("id", Integer, primary_key=True, autoincrement=False),
        Column("name", Text, nullable=False),
        Column("country", Text, nullable=False),
        Column("born_at", Date, nullable=False),
    )
    books = Table(
        "books", md,
        Column("id", Integer, primary_key=True, autoincrement=False),
        Column("author_id", Integer, ForeignKey("authors.id"), nullable=False),
        Column("title", Text, nullable=False),
        Column("published", Date, nullable=False),
        Column("price", Numeric(8, 2), nullable=False),
        Column("pages", Integer, nullable=False),
    )
    reviews = Table(
        "reviews", md,
        Column("id", Integer, primary_key=True, autoincrement=False),
        Column("book_id", Integer, ForeignKey("books.id"), nullable=False),
        Column("rating", SmallInteger, nullable=False),
        Column("body", Text, nullable=False),
        Column("created_at", DateTime(timezone=True), nullable=False),
    )
    Index("books_author_id_idx", books.c.author_id)
    Index("reviews_book_id_idx", reviews.c.book_id)
    return md


# --------------------------------------------------------------------------
# Изменения схемы. Подобраны так, чтобы задеть РАЗНЫЕ механизмы сравнения, а
# не шесть раз один и тот же.
# --------------------------------------------------------------------------

def case_add_column_with_default(md):
    md.tables["books"].append_column(
        Column("in_stock", Integer, nullable=False, server_default=text("0")))


def case_change_type(md):
    md.tables["books"].c.pages.type = BigInteger()


def case_rename_index(md):
    books = md.tables["books"]
    for idx in list(books.indexes):
        if idx.name == "books_author_id_idx":
            books.indexes.discard(idx)
    Index("books_by_author_idx", books.c.author_id)


def case_add_check(md):
    md.tables["reviews"].append_constraint(
        CheckConstraint("rating BETWEEN 1 AND 5", name="reviews_rating_range"))


def case_add_partial_index(md):
    reviews = md.tables["reviews"]
    Index("reviews_low_rating_idx", reviews.c.book_id,
          postgresql_where=text("rating <= 2"))


def case_shrink_text(md):
    md.tables["authors"].c.country.type = String(64)


def case_change_existing_default(md):
    """Умолчание меняется у СУЩЕСТВУЮЩЕГО столбца.

    Именно этот случай проверяет compare_server_default, а не добавление
    нового столбца с умолчанием: у нового столбца умолчание приезжает
    вместе с ним и видно безо всякого сравнения умолчаний.

    DefaultClause, а не просто text(): первая версия присваивала сырой
    TextClause, Alembic его за умолчание не считал, и случай показывал
    «изменение не обнаружено» в обоих режимах. Вывод «Alembic не видит
    смену умолчания» был бы при этом ложным — не видел мой случай.
    """
    md.tables["books"].c.pages.server_default = DefaultClause(text("100"))


def case_rename_column(md):
    """Переименование столбца — известная граница автогенерации.

    Она сравнивает НАБОРЫ столбцов, а не их судьбу, и переименование видит
    как «одного не стало, другой появился». Вопрос не в том, заметит ли она
    разницу — заметит, — а в том, что она предложит сделать.
    """
    books = md.tables["books"]
    books.c.title.name = "name"
    books.c.title.key = "name"


CASES = (
    ("контроль: изменений нет", None),
    ("столбец с server_default", case_add_column_with_default),
    ("смена типа integer -> bigint", case_change_type),
    ("переименование индекса", case_rename_index),
    ("ограничение CHECK", case_add_check),
    ("частичный индекс", case_add_partial_index),
    ("сужение типа text -> varchar(64)", case_shrink_text),
    ("смена умолчания у столбца", case_change_existing_default),
    ("переименование столбца", case_rename_column),
)

MODES = (
    ("умолчания", {}),
    ("строгое сравнение", {"compare_type": True,
                           "compare_server_default": True}),
)

SNAPSHOT_SQL = (
    # Столбцы: тип, длина и точность, обязательность, умолчание. Умолчание
    # здесь не для полноты: server_default — как раз то, чего Alembic по
    # умолчанию не сравнивает.
    """
    SELECT 'column' AS kind, table_name || '.' || column_name AS name,
           concat_ws(' ', data_type, character_maximum_length,
                     numeric_precision, numeric_scale, is_nullable,
                     column_default) AS body
    FROM information_schema.columns
    WHERE table_schema = 'public'
    """,
    # Индексы целиком определением: переименование и частичность видны тут.
    """
    SELECT 'index', indexname, indexdef
    FROM pg_indexes WHERE schemaname = 'public'
    """,
    # Ограничения определением: CHECK, внешние и первичные ключи.
    """
    SELECT 'constraint', conname, pg_get_constraintdef(oid)
    FROM pg_constraint WHERE connamespace = 'public'::regnamespace
    """,
)


def flatten(ops):
    """Разворачивает контейнерные операции в исполнимые.

    produce_migrations возвращает дерево: ModifyTableOps группирует правки
    одной таблицы и сам по себе не исполним — Operations.invoke на нём
    падает с «no dispatch function for object». Наружу должны выходить
    листья.
    """
    for op in ops:
        sub = getattr(op, "ops", None)
        if sub:
            yield from flatten(sub)
        else:
            yield op


def dump_schema(engine):
    """Снимок схемы из системного каталога.

    Строки сортируются: без этого два снимка одной и той же схемы
    отличались бы порядком и сравнение всегда давало бы «не восстановлено».
    """
    parts = []
    with engine.connect() as conn:
        for query in SNAPSHOT_SQL:
            for kind, name, body in conn.execute(text(query)).all():
                parts.append(f"{kind}|{name}|{body}")
    return sorted(parts)


def run_case(engine, title, mutate, mode_name, opts):
    md = baseline()
    if mutate is not None:
        mutate(md)

    before = dump_schema(engine)
    applied = restored = None
    error = None

    with engine.connect() as conn:
        mc = MigrationContext.configure(conn, opts=dict(opts))
        script = produce_migrations(mc, md)
        ops = script.upgrade_ops
        detected = bool(ops.ops)
        rendered = render_python_code(ops).strip() if detected else ""

        if detected:
            try:
                operations = Operations(mc)
                for op in flatten(ops.ops):
                    operations.invoke(op)
                conn.commit()
                applied = True
            except Exception as exc:  # noqa: BLE001
                conn.rollback()
                applied = False
                error = f"{type(exc).__name__}: {str(exc).splitlines()[0][:160]}"

    after_upgrade = dump_schema(engine) if applied else before

    if applied:
        with engine.connect() as conn:
            mc = MigrationContext.configure(conn, opts=dict(opts))
            operations = Operations(mc)
            try:
                for op in flatten(ops.reverse().ops):
                    operations.invoke(op)
                conn.commit()
            except Exception as exc:  # noqa: BLE001
                conn.rollback()
                tail = f"{type(exc).__name__}: {str(exc).splitlines()[0][:160]}"
                error = f"{error} | откат: {tail}" if error else f"откат: {tail}"
        restored = dump_schema(engine) == before

    return {
        "case": title,
        "mode": mode_name,
        "detected": detected,
        "applied": applied,
        "restored": restored,
        "upgrade_changed_schema": (after_upgrade != before)
        if applied else None,
        "operations": [type(o).__name__ for o in flatten(ops.ops)],
        "rendered": rendered[:400],
        "error": error,
        "alembic": common.versions()["alembic"],
        "checked_at": dt.datetime.now(dt.timezone.utc).isoformat(),
    }


def main():
    out_path = os.environ.get("OUT", "/fixtures/alembic.txt")
    engine = create_engine(common.dsn_for("psycopg"))
    # Снимок до всего: сценарий меняет схему, и оставленное изменение
    # обесценило бы остальные фикстуры стенда.
    pristine = dump_schema(engine)
    rows = []
    try:
        for mode_name, opts in MODES:
            for title, mutate in CASES:
                row = run_case(engine, title, mutate, mode_name, opts)
                rows.append(row)
                print(json.dumps(row, ensure_ascii=False, sort_keys=True))
                if title.startswith("контроль") and row["detected"]:
                    print("КОНТРОЛЬ НЕ ПРОЙДЕН: на неизменённом описании "
                          "автогенерация предложила операции "
                          f"{row['operations']} — описание расходится с базой, "
                          "остальные случаи не читаются", file=sys.stderr)
                    return 2

        final = dump_schema(engine)
    finally:
        engine.dispose()

    with open(out_path, "a", encoding="utf-8") as out:
        for row in rows:
            out.write(json.dumps(row, ensure_ascii=False,
                                 sort_keys=True) + "\n")

    if final != pristine:
        print("СЦЕНАРИЙ ОСТАВИЛ СХЕМУ ИЗМЕНЁННОЙ — пересейте базу "
              "(scripts/seed.sh)", file=sys.stderr)
        for line in sorted(set(final) ^ set(pristine)):
            print("  " + line, file=sys.stderr)
        return 3

    print()
    print(f"схема после сценария совпадает с исходной "
          f"({len(pristine)} записей в снимке)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
