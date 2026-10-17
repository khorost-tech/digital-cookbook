"""Набор на моках — написанный добросовестно, а не в поддавки.

ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ. Мок не выполняет запрос, поэтому проверить им
результат правила нечем: результат придумывает сам тест. Остаётся
проверять взаимодействие — что репозиторий сходил в соединение, что
вернул полученное, что в запрос попали нужные таблица и колонки.

ЭТО НЕ КАРИКАТУРА. Тесты ниже написаны настолько строго, насколько мок
вообще позволяет: они разбирают текст сформированного SQL и требуют,
чтобы в нём было то, что диктует правило. Слабое место такого набора
не в лени автора, а в том, что смысл запроса определяет СУБД, а её
здесь нет.
"""

from unittest.mock import MagicMock

import repo as repo_mod


def make_repo(rows):
    conn = MagicMock()
    conn.execute.return_value = rows
    return repo_mod.TaskRepo(conn), conn


def test_unfinished():
    repo, conn = make_repo([(2,), (3,), (4,)])
    assert repo.unfinished() == [2, 3, 4]
    conn.execute.assert_called_once()
    sql = str(conn.execute.call_args[0][0])
    assert "tasks" in sql and "status" in sql


def test_search_is_case_insensitive():
    repo, conn = make_repo([(4,)])
    assert repo.search("summa") == [4]
    sql = str(conn.execute.call_args[0][0])
    assert "title" in sql and "LIKE" in sql.upper()


def test_per_author():
    row = MagicMock()
    row.author, row.total, row.done = "lem", 2, 0
    repo, conn = make_repo([row])
    result = repo.per_author()
    # Порядок утверждений важен: число обращений проверяется ПЕРВЫМ.
    # Это единственное, что мок способен увидеть и чего не видит ни один
    # набор, сверяющий результат: запрос на каждого автора возвращает то
    # же самое, только за N обращений вместо одного.
    assert conn.execute.call_count == 1, (
        "правило — один запрос, а не запрос на автора")
    assert result == [("lem", 2, 0)]
    sql = str(conn.execute.call_args[0][0])
    assert "GROUP BY" in sql.upper()
