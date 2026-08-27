"""ПРИЁМ 4: Testcontainers — тот же postgres:18.1-alpine, что в Go- и
Java-частях стенда. В этом и смысл «общего знаменателя»: API у трёх библиотек
разный, контейнер и проверяемое поведение — одни и те же.

    pytest -m integration
"""

from unittest.mock import MagicMock

import psycopg
import pytest
from testcontainers.postgres import PostgresContainer

from service import Order, OrderService
from store import SCHEMA, PostgresStore

pytestmark = pytest.mark.integration


@pytest.fixture(scope="module")
def dsn():
    with PostgresContainer("postgres:18.1-alpine", driver=None) as pg:
        url = pg.get_connection_url()
        with psycopg.connect(url) as conn:
            conn.execute(SCHEMA)
        yield url


@pytest.fixture(autouse=True)
def _clean(dsn):
    # Изоляция: каждый тест начинает с чистого листа.
    with psycopg.connect(dsn) as conn:
        conn.execute("TRUNCATE orders")


def test_заказ_доезжает_до_postgres(dsn) -> None:
    store = PostgresStore(dsn)
    svc = OrderService(store, MagicMock())

    svc.create("o-1", "u-1", 25_000)

    got = store.by_user("u-1")
    assert len(got) == 1
    assert got[0].price_cent == 22_500, "250.00 минус 10%"


def test_дубль_id_отклоняется_базой(dsn) -> None:
    """Первичный ключ — то, чего фейк не знает вовсе."""
    store = PostgresStore(dsn)
    o = Order(id="o-1", user_id="u-1", total_cent=10_000, price_cent=9_500)
    store.save(o)

    with pytest.raises(psycopg.errors.UniqueViolation):
        store.save(o)
