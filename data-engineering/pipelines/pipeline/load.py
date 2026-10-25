"""Выгрузка дня из источника и загрузка в dwh.raw — в разных режимах.

Режимы загрузки (ось 2):
  append        INSERT пакетами, фиксация после каждого пакета;
  delete-insert DELETE дня и INSERT в одной транзакции;
  delete-batch  DELETE дня фиксируется сразу, INSERT — пакетами с фиксацией;
  merge         INSERT … ON CONFLICT (id) DO UPDATE пакетами с фиксацией.
crash_after — упасть после стольких пакетов позиций (имитация падения задачи).
"""

import datetime as dt
import os

import psycopg2

BATCH = 50


class Crash(RuntimeError):
    """Искусственное падение задачи посреди загрузки."""


def day_bounds(day):
    start = dt.datetime.combine(day, dt.time())
    return start, start + dt.timedelta(days=1)


def extract(day, by="created_at"):
    """Заказы и позиции дня из источника: по времени события или поступления."""
    assert by in ("created_at", "ingested_at")
    start, end = day_bounds(day)
    with psycopg2.connect(os.environ["STAND_SHOP_DSN"]) as c, c.cursor() as cur:
        cur.execute("SELECT id, customer_id, status, created_at, ingested_at FROM app.orders "
                    "WHERE %s >= %%s AND %s < %%s ORDER BY id" % (by, by), (start, end))
        orders = cur.fetchall()
        cur.execute("SELECT i.id, i.order_id, i.product_id, i.qty, i.price FROM app.order_items i "
                    "JOIN app.orders o ON o.id = i.order_id WHERE o.%s >= %%s AND o.%s < %%s ORDER BY i.id"
                    % (by, by), (start, end))
        items = cur.fetchall()
    return orders, items


DDL = """CREATE SCHEMA IF NOT EXISTS raw;
CREATE TABLE IF NOT EXISTS raw.orders (id bigint, customer_id int, status text, created_at timestamp,
  ingested_at timestamp, load_day date NOT NULL);
CREATE TABLE IF NOT EXISTS raw.order_items (id bigint, order_id bigint, product_id int, qty int, price int,
  load_day date NOT NULL);"""
DDL_KEYS = """CREATE UNIQUE INDEX IF NOT EXISTS orders_id ON raw.orders (id);
CREATE UNIQUE INDEX IF NOT EXISTS order_items_id ON raw.order_items (id);"""


def dwh():
    return psycopg2.connect(os.environ["STAND_DWH_DSN"])


def ensure(keys=False):
    with dwh() as c, c.cursor() as cur:
        cur.execute(DDL)
        if keys:
            cur.execute(DDL_KEYS)


def _insert(cur, table, rows, day, upsert):
    cols = "(id, customer_id, status, created_at, ingested_at, load_day)" if table == "raw.orders" else \
        "(id, order_id, product_id, qty, price, load_day)"
    marks = ",".join(["%s"] * 6)
    sql = "INSERT INTO %s %s VALUES (%s)" % (table, cols, marks)
    if upsert:
        sets = ", ".join("%s = EXCLUDED.%s" % (c, c) for c in cols.strip("()").split(", ")[1:])
        sql += " ON CONFLICT (id) DO UPDATE SET " + sets
    cur.executemany(sql, [tuple(r) + (day,) for r in rows])


def load(day, orders, items, mode, crash_after=None):
    """Загрузка дня в raw; возвращает число зафиксированных пакетов позиций."""
    ensure(keys=(mode == "merge"))
    c = dwh()
    try:
        cur = c.cursor()
        if mode in ("delete-insert", "delete-batch"):
            cur.execute("DELETE FROM raw.order_items WHERE load_day = %s", (day,))
            cur.execute("DELETE FROM raw.orders WHERE load_day = %s", (day,))
            if mode == "delete-batch":
                c.commit()
        _insert(cur, "raw.orders", orders, day, upsert=(mode == "merge"))
        if mode != "delete-insert":
            c.commit()
        batches = 0
        for k in range(0, len(items), BATCH):
            _insert(cur, "raw.order_items", items[k:k + BATCH], day, upsert=(mode == "merge"))
            if mode != "delete-insert":
                c.commit()
            batches += 1
            if crash_after is not None and batches == crash_after:
                raise Crash("задача упала после %d пакетов позиций" % batches)
        c.commit()
        return batches
    except Exception:
        c.rollback()
        raise
    finally:
        c.close()
