"""Репозиторий с четырьмя переключаемыми дефектами.

ЗАЧЕМ ДЕФЕКТЫ ЗАДАЮТСЯ ПЕРЕМЕННОЙ, А НЕ ПРАВКОЙ ФАЙЛА. Сравнение имеет
смысл только тогда, когда все три набора тестов гоняются по ОДНОМУ И ТОМУ
ЖЕ коду. Если исправлять руками между прогонами, «поймал» и «не поймал»
окажутся про разные версии репозитория, и таблица будет ни о чём.

ДЕФЕКТЫ ВЫБРАНЫ НЕ СЛУЧАЙНО. Каждый ломает бизнес-правило, но заметен на
разной глубине:

    none         правильная реализация — контроль
    null-filter  <> вместо IS DISTINCT FROM: строки с NULL молча выпадают
    like-case    LIKE вместо регистронезависимого поиска
    group-by     несгруппированная колонка в SELECT ... GROUP BY
    n-plus-1     запрос на каждого автора вместо одного запроса

Первый дефект одинаково виден и на SQLite, и на PostgreSQL — SQLite
трактует NULL так же. Второй и третий SQLite пропускает: у него LIKE
регистронезависим для ASCII, а несгруппированную колонку он разрешает.

ПОСЛЕДНИЙ ДЕФЕКТ ЗДЕСЬ НЕ ДЛЯ СИММЕТРИИ. Он не меняет результат, поэтому
оба набора, проверяющие результат, остаются зелёными — и на настоящей
базе тоже. Видит его только набор на моках, который считает обращения к
соединению. Без такой строки таблица получилась бы подставной: «моки не
ловят ничего» звучит убедительно ровно до вопроса, а могли ли они хоть
что-нибудь поймать в принципе.
"""

import os

from sqlalchemy import (Column, Integer, MetaData, String, Table, case, func,
                        select)

DEFECT = os.environ.get("DEFECT", "none")

from defects import DEFECTS, WHAT  # noqa: F401

meta = MetaData()
tasks = Table(
    "tasks", meta,
    Column("id", Integer, primary_key=True),
    Column("author", String(64), nullable=False),
    Column("title", String(200), nullable=False),
    # status допускает NULL намеренно: «статус ещё не проставлен» —
    # обычное состояние, и правило про незавершённые обязано его учитывать.
    Column("status", String(32), nullable=True),
)

SEED = [
    {"id": 1, "author": "strugatsky", "title": "Полдень, XXI век",
     "status": "done"},
    {"id": 2, "author": "strugatsky", "title": "Улитка на склоне",
     "status": "wip"},
    {"id": 3, "author": "lem", "title": "Солярис", "status": None},
    # Название латиницей и в верхнем регистре — намеренно. Проверка
    # регистронезависимого поиска обязана падать из-за ДЕФЕКТА, а не из-за
    # того, что lower() в SQLite работает только с ASCII: на кириллице
    # правильная реализация провалилась бы у фейка, и в таблице появилась
    # бы «пойманная» ошибка, которой в коде нет.
    {"id": 4, "author": "lem", "title": "SUMMA TECHNOLOGIAE",
     "status": "wip"},
]


class TaskRepo:
    """Три правила предметной области. Каждое — один запрос."""

    def __init__(self, connection, defect=None):
        self.c = connection
        self.defect = DEFECT if defect is None else defect

    def unfinished(self):
        """ПРАВИЛО: незавершённые — это всё, что не «done».

        Задача без статуса тоже не завершена: статус неизвестен, значит
        работа не сдана. Ровно это и ломает дефект — <> отбрасывает NULL,
        потому что NULL <> 'done' даёт не «истину», а NULL.
        """
        if self.defect == "null-filter":
            where = tasks.c.status != "done"
        else:
            where = tasks.c.status.is_distinct_from("done")
        stmt = select(tasks.c.id).where(where).order_by(tasks.c.id)
        return [row[0] for row in self.c.execute(stmt)]

    def search(self, term):
        """ПРАВИЛО: поиск по названию не зависит от регистра."""
        pattern = f"%{term}%"
        if self.defect == "like-case":
            where = tasks.c.title.like(pattern)
        else:
            where = func.lower(tasks.c.title).like(func.lower(pattern))
        stmt = select(tasks.c.id).where(where).order_by(tasks.c.id)
        return [row[0] for row in self.c.execute(stmt)]

    def per_author(self):
        """ПРАВИЛО: по автору — сколько задач и сколько из них завершено.

        Дефект добавляет в выборку несгруппированную колонку title.
        PostgreSQL на этом останавливается, SQLite молча берёт значение
        из произвольной строки группы.
        """
        done = func.sum(case((tasks.c.status == "done", 1), else_=0))
        if self.defect == "n-plus-1":
            return self._per_author_n_plus_1(done)
        columns = [tasks.c.author, func.count().label("total"),
                   done.label("done")]
        if self.defect == "group-by":
            columns.append(tasks.c.title)
        stmt = (select(*columns).group_by(tasks.c.author)
                .order_by(tasks.c.author))
        return [(r.author, int(r.total), int(r.done))
                for r in self.c.execute(stmt)]

    def _per_author_n_plus_1(self, done):
        """Тот же ответ, но запросом на каждого автора.

        Результат совпадает с правильным до последнего байта — потому
        этот дефект и не виден наборам, которые сверяют результат. Он
        виден только по числу обращений к соединению.
        """
        authors = [r[0] for r in self.c.execute(
            select(tasks.c.author).distinct().order_by(tasks.c.author))]
        out = []
        for author in authors:
            rows = list(self.c.execute(
                select(func.count().label("total"), done.label("done"))
                .where(tasks.c.author == author)))
            out.append((author, int(rows[0].total), int(rows[0].done)))
        return out


def create_and_seed(connection):
    meta.create_all(connection)
    connection.execute(tasks.insert(), SEED)
