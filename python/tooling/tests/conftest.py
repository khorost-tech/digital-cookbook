"""Общие приспособления: движки для фейка и для настоящей базы.

ОБЛАСТЬ ВИДИМОСТИ КОНТЕЙНЕРА ЗАДАЁТСЯ КЛЮЧОМ, а не прибита в коде.
Именно она определяет цену интеграционного набора: при области function
контейнер поднимается на каждый тест, при session — один раз на весь
прогон. Разница в разы, и её надо показать числом, а не рассуждением.

Старты контейнера СЧИТАЮТСЯ, а не выводятся из области видимости.
«Session — значит один старт» звучит очевидно ровно до первой ошибки в
приспособлении; счётчик пишется в файл и уходит в фикстуру замера.
"""

import json
import os

import pytest
import sqlalchemy

import repo as repo_mod

STARTS = {"postgres": 0}
# Сколько раз выполнилось тело property-based теста. Счётчик здесь, а не
# в самом тесте: его надо записать в файл ПОСЛЕ прогона, и тело теста об
# этом знать не обязано.
EXAMPLES = {"n": 0}
STARTS_PATH = os.environ.get("STARTS_OUT", "")


def pytest_addoption(parser):
    parser.addoption("--pg-scope", default="session",
                     choices=("function", "session"),
                     help="область видимости контейнера PostgreSQL")


def pg_scope(fixture_name, config):
    """Динамическая область: pytest разрешает задать её функцией."""
    return config.getoption("--pg-scope")


@pytest.fixture(scope="function")
def sqlite_conn():
    """Фейк: настоящий SQL-движок, но не тот, что в проде.

    Он не «почти PostgreSQL». Это отдельная СУБД со своими правилами, и
    ровно в этом смысл плеча: часть дефектов она ловит по-настоящему, а
    часть пропускает — тоже по-настоящему.
    """
    engine = sqlalchemy.create_engine("sqlite+pysqlite:///:memory:")
    with engine.begin() as conn:
        repo_mod.create_and_seed(conn)
        yield conn


@pytest.fixture(scope=pg_scope)
def pg_container(request):
    # testcontainers.community.postgres, а НЕ testcontainers.postgres.
    # Второй путь в 4.15.0 оставлен ради совместимости и на каждом импорте
    # печатает DeprecationWarning. Стенд про современные практики не может
    # показывать читателю устаревший вызов, поэтому предупреждения здесь
    # возведены в ошибки (см. pytest.ini): если путь снова устареет,
    # прогон покраснеет, а не промолчит.
    from testcontainers.community.postgres import PostgresContainer

    container = PostgresContainer("postgres:18", driver="psycopg")
    container.start()
    STARTS["postgres"] += 1
    request.addfinalizer(container.stop)
    return container


@pytest.fixture(scope="function")
def pg_conn(pg_container):
    """Соединение в транзакции, которая всегда откатывается.

    При области session контейнер один на весь прогон, и без отката
    тесты видели бы данные друг друга. Тогда порядок тестов начал бы
    влиять на результат — а замер как раз о том, кто что ловит, и
    посторонняя связь между тестами его бы испортила.
    """
    engine = sqlalchemy.create_engine(pg_container.get_connection_url())
    conn = engine.connect()
    trans = conn.begin()
    repo_mod.create_and_seed(conn)
    try:
        yield conn
    finally:
        trans.rollback()
        conn.close()
        engine.dispose()


def pytest_sessionfinish(session, exitstatus):
    if STARTS_PATH:
        with open(STARTS_PATH, "w", encoding="utf-8") as f:
            json.dump({**STARTS, "examples": EXAMPLES["n"]}, f)
