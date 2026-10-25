"""Генератор источника и эталона: синтетический магазин, фиксированное зерно.

Данные строятся в памяти детерминированно, эталон считается по ним же — не по
базе и не по хранилищу пайплайна. В базу источника (shop.app) данные пишутся
отдельно; так проверка «хранилище совпало с эталоном» не сравнивает таблицу
саму с собой.

    python3 -m generator.gen reset            создать схему app и загрузить 14 дней
    python3 -m generator.gen late D N [LAG]   добавить N поздних заказов за день D, приход через LAG дней
    python3 -m generator.gen truth RULE       эталон по дням (JSON), RULE = v1 | v2
"""

import datetime as dt
import json
import os
import random
import sys

import psycopg2

SEED = 20261001
DAYS = [dt.date(2026, 9, 1) + dt.timedelta(days=i) for i in range(14)]
CUSTOMERS, PRODUCTS = 200, 50


def build(seed=SEED):
    """Заказы и позиции: [(order)], [(item)] — детерминированно по зерну."""
    rnd = random.Random(seed)
    prices = {p: rnd.randint(100, 5000) * 10 for p in range(1, PRODUCTS + 1)}  # в копейках
    orders, items = [], []
    oid = iid = 0
    for day in DAYS:
        for _ in range(rnd.randint(250, 350)):
            oid += 1
            created = dt.datetime.combine(day, dt.time()) + dt.timedelta(seconds=rnd.randint(0, 86399))
            status = rnd.choices(["paid", "cancelled", "refunded"], [85, 10, 5])[0]
            orders.append((oid, rnd.randint(1, CUSTOMERS), status, created, created))
            for _ in range(rnd.randint(1, 4)):
                iid += 1
                pid = rnd.randint(1, PRODUCTS)
                items.append((iid, oid, pid, rnd.randint(1, 3), prices[pid]))
    return orders, items


def late_orders(day, n, start_oid, start_iid, seed=SEED, lag=1):
    """Поздние заказы: событие в последний час дня D, в источник — через lag дней в 02:00."""
    rnd = random.Random(seed + day.toordinal())
    orders, items = [], []
    for k in range(n):
        oid, created = start_oid + k + 1, dt.datetime.combine(day, dt.time(23, 0)) + dt.timedelta(minutes=k)
        arrived = dt.datetime.combine(day + dt.timedelta(days=lag), dt.time(2, 0)) + dt.timedelta(minutes=k)
        orders.append((oid, rnd.randint(1, CUSTOMERS), "paid", created, arrived))
        items.append((start_iid + k + 1, oid, 1, 1, 10000))
    return orders, items


def revenue(orders, items, rule):
    """Эталон по дням: выручка (копейки), заказы, позиции. v1 — без отменённых; v2 — ещё и без возвратов."""
    excluded = {"v1": {"cancelled"}, "v2": {"cancelled", "refunded"}}[rule]
    by_order = {}
    for _, oid, _, qty, price in items:
        by_order.setdefault(oid, [0, 0])
        by_order[oid][0] += qty * price
        by_order[oid][1] += 1
    out = {}
    for oid, _, status, created, _ in orders:
        d = out.setdefault(created.date().isoformat(), {"revenue": 0, "orders": 0, "items": 0})
        d["orders"] += 1
        d["items"] += by_order.get(oid, [0, 0])[1]
        if status not in excluded:
            d["revenue"] += by_order.get(oid, [0, 0])[0]
    return out


def connect():
    return psycopg2.connect(os.environ["STAND_SHOP_DSN"])


def reset():
    orders, items = build()
    with connect() as c, c.cursor() as cur:
        cur.execute("CREATE EXTENSION IF NOT EXISTS pg_stat_statements")
        cur.execute("DROP SCHEMA IF EXISTS app CASCADE; CREATE SCHEMA app;")
        cur.execute("""CREATE TABLE app.orders (id bigint PRIMARY KEY, customer_id int NOT NULL,
                         status text NOT NULL, created_at timestamp NOT NULL, ingested_at timestamp NOT NULL);
                       CREATE TABLE app.order_items (id bigint PRIMARY KEY, order_id bigint NOT NULL,
                         product_id int NOT NULL, qty int NOT NULL, price int NOT NULL)""")
        cur.executemany("INSERT INTO app.orders VALUES (%s,%s,%s,%s,%s)", orders)
        cur.executemany("INSERT INTO app.order_items VALUES (%s,%s,%s,%s,%s)", items)
    return len(orders), len(items)


def add_late(day, n, lag=1):
    orders, items = build()
    lo, li = late_orders(day, n, len(orders), len(items), lag=lag)
    with connect() as c, c.cursor() as cur:
        cur.executemany("INSERT INTO app.orders VALUES (%s,%s,%s,%s,%s)", lo)
        cur.executemany("INSERT INTO app.order_items VALUES (%s,%s,%s,%s,%s)", li)


def truth(rule, late=None):
    """Эталон; late = (день, n) — с учётом поздних заказов."""
    orders, items = build()
    if late:
        lo, li = late_orders(dt.date.fromisoformat(late[0]), late[1], len(orders), len(items))
        orders, items = orders + lo, items + li
    return revenue(orders, items, rule)


def main():
    cmd = sys.argv[1]
    if cmd == "reset":
        print(json.dumps(dict(zip(("orders", "items"), reset()))))
    elif cmd == "late":
        add_late(dt.date.fromisoformat(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]) if len(sys.argv) > 4 else 1)
    elif cmd == "truth":
        late = (sys.argv[3], int(sys.argv[4])) if len(sys.argv) > 4 else None
        print(json.dumps(truth(sys.argv[2], late)))


if __name__ == "__main__":
    main()
