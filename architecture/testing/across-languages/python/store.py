"""Реализация Store поверх Postgres — для интеграционного теста."""

from __future__ import annotations

import psycopg

from service import Order

SCHEMA = """
CREATE TABLE IF NOT EXISTS orders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT   NOT NULL,
    total_cent BIGINT NOT NULL,
    price_cent BIGINT NOT NULL
)"""


class PostgresStore:
    def __init__(self, dsn: str) -> None:
        self._dsn = dsn

    def save(self, o: Order) -> None:
        with psycopg.connect(self._dsn) as conn:
            conn.execute(
                "INSERT INTO orders (id, user_id, total_cent, price_cent) VALUES (%s, %s, %s, %s)",
                (o.id, o.user_id, o.total_cent, o.price_cent),
            )

    def by_user(self, user_id: str) -> list[Order]:
        with psycopg.connect(self._dsn) as conn:
            rows = conn.execute(
                "SELECT id, user_id, total_cent, price_cent FROM orders WHERE user_id = %s ORDER BY id",
                (user_id,),
            ).fetchall()
        return [Order(id=r[0], user_id=r[1], total_cent=r[2], price_cent=r[3]) for r in rows]
