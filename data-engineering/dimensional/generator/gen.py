"""Генератор источника и эталона для звёздной схемы, фиксированное зерно.

Магазин за 14 дней сентября 2026 (как у стенда pipelines) плюс события
измерений: клиенты переезжают между регионами (один или два раза), у товаров
категории, по товарам — ежедневный остаток на складе, id клиента 77
переиспользуется 07.09 новым человеком, клиент 201 делает заказы 10.09, а в
источнике появляется 11.09. Источник хранит только текущее состояние клиента
(регион и updated_at) — историю строит хранилище. Эталон считается по полной
истории генератора, не по базе.

    python3 -m generator.gen reset            схема app: товары, заказы, позиции, склад
    python3 -m generator.gen customers-at D   app.customers — состояние источника на конец дня D
    python3 -m generator.gen truth            эталон (JSON)
"""

import datetime as dt
import json
import os
import random
import sys

import psycopg2

SEED = 20260901
DAYS = [dt.date(2026, 9, 1) + dt.timedelta(days=i) for i in range(14)]
REGIONS = ["north", "south", "east", "west", "center"]
CATEGORIES = ["books", "toys", "garden", "kitchen", "sport", "tech"]
CUSTOMERS, PRODUCTS = 200, 50
RECYCLED, RECYCLED_DAY = 77, dt.date(2026, 9, 7)
LATE, LATE_ORDER_DAY, LATE_KNOWN_DAY = 201, dt.date(2026, 9, 10), dt.date(2026, 9, 11)
WEEK = DAYS[7:]  # 08–14.09 — неделя для мер склада


def build(seed=SEED):
    rnd = random.Random(seed)
    # история клиента: [(с какого дня, регион, человек)]; человек различает двух владельцев id 77
    history = {c: [(DAYS[0], rnd.choice(REGIONS), "%d" % c)] for c in range(1, CUSTOMERS + 1)}
    movers = rnd.sample([c for c in history if c != RECYCLED], 40)
    for i, c in enumerate(movers):
        days = sorted(rnd.sample(DAYS[1:], 1 if i < 30 else 2))
        for d in days:
            cur = history[c][-1][1]
            history[c].append((d, rnd.choice([r for r in REGIONS if r != cur]), "%d" % c))
    old = history[RECYCLED][-1][1]
    history[RECYCLED].append((RECYCLED_DAY, rnd.choice([r for r in REGIONS if r != old]), "%db" % RECYCLED))
    history[LATE] = [(LATE_KNOWN_DAY, rnd.choice(REGIONS), "%d" % LATE)]

    products = {p: (rnd.choice(CATEGORIES), rnd.randint(100, 5000) * 10) for p in range(1, PRODUCTS + 1)}
    orders, items = [], []
    oid = iid = 0
    for day in DAYS:
        n = rnd.randint(250, 350)
        buyers = [rnd.randint(1, CUSTOMERS) for _ in range(n)] + ([LATE] * 5 if day == LATE_ORDER_DAY else [])
        for cust in buyers:
            oid += 1
            created = dt.datetime.combine(day, dt.time()) + dt.timedelta(seconds=rnd.randint(0, 86399))
            status = rnd.choices(["paid", "cancelled", "refunded"], [85, 10, 5])[0]
            orders.append((oid, cust, status, created))
            for pid in rnd.sample(range(1, PRODUCTS + 1), rnd.randint(1, 4)):
                iid += 1
                items.append((iid, oid, pid, rnd.randint(1, 3), products[pid][1]))

    stock = {p: rnd.randint(150, 300) for p in products}
    inventory = []
    sold = {}
    for o_id, _, status, created in orders:
        if status != "cancelled":
            sold.setdefault(created.date(), {})
    by_order = {o[0]: o for o in orders}
    for _, o_id, pid, qty, _ in items:
        o = by_order[o_id]
        if o[2] != "cancelled":
            day = sold.setdefault(o[3].date(), {})
            day[pid] = day.get(pid, 0) + qty
    for day in DAYS:
        for p in products:
            stock[p] -= sold.get(day, {}).get(p, 0)
            if stock[p] < 50:
                stock[p] += 200  # поставка
            inventory.append((day, p, stock[p]))
    return history, products, orders, items, inventory


def state(history, day):
    """Источник на конец дня: {id: (регион, updated_at, имя)} — только известные."""
    out = {}
    for c, h in history.items():
        known = [e for e in h if e[0] <= day]
        if known:
            frm, region, person = known[-1]
            out[c] = (region, dt.datetime.combine(frm, dt.time()), "customer-%s" % person)
    return out


def region_at(history, c, day):
    """Регион человека на момент заказа; клиент, известный позже заказа, — по первой записи."""
    h = history[c]
    known = [e for e in h if e[0] <= day]
    return (known[-1] if known else h[0])[1]


def person_at(history, c, day):
    h = history[c]
    known = [e for e in h if e[0] <= day]
    return (known[-1] if known else h[0])[2]


def truth():
    history, products, orders, items, inventory = build()
    amount, by_cat = {}, {}
    order_of = {o[0]: o for o in orders}
    for _, o_id, pid, qty, price in items:
        o = order_of[o_id]
        if o[2] == "cancelled":
            continue
        amount[o_id] = amount.get(o_id, 0) + qty * price
        cat = products[pid][0]
        by_cat[cat] = by_cat.get(cat, 0) + qty * price
    asof, current, person_last = {}, {}, {}
    order_asof, order_current = {}, {}  # регион каждого заказа: агрегат прячет встречные переносы
    for c, h in history.items():
        for _, region, person in h:
            person_last[person] = region  # последний регион каждого человека
    by_person_orders, by_person_revenue = {}, {}
    for o_id, c, status, created in orders:
        p = person_at(history, c, created.date())
        by_person_orders[p] = by_person_orders.get(p, 0) + 1
        by_person_revenue[p] = by_person_revenue.get(p, 0) + amount.get(o_id, 0)
        if status == "cancelled":
            continue
        r = region_at(history, c, created.date())
        asof[r] = asof.get(r, 0) + amount.get(o_id, 0)
        cr = person_last[p]
        order_asof[str(o_id)], order_current[str(o_id)] = r, cr
        current[cr] = current.get(cr, 0) + amount.get(o_id, 0)
    moved_once = sorted(c for c, h in history.items() if len({e[1] for e in h}) == 2 and len(h) == 2 and c != RECYCLED)
    moved_twice = sorted(c for c, h in history.items() if len(h) == 3)
    week_last = sum(s for d, _, s in inventory if d == WEEK[-1])
    week_avg = sum(s for d, _, s in inventory if d in WEEK) / len(WEEK)
    week_sum = sum(s for d, _, s in inventory if d in WEEK)
    shares = []
    for day in DAYS:
        day_orders = [o for o in orders if o[3].date() == day]
        shares.append(sum(o[2] == "cancelled" for o in day_orders) / len(day_orders))
    total = len(orders)
    return {
        "revenue_by_region_asof": asof, "revenue_by_region_current": current,
        "order_region_asof": order_asof, "order_region_current": order_current,
        "revenue_by_category": by_cat, "revenue_total": sum(amount.values()),
        "orders": total, "items": len(items),
        "moved_once": moved_once, "moved_twice": moved_twice,
        "previous_region": {str(c): history[c][-2][1] for c in moved_once + moved_twice},
        "first_region": {str(c): history[c][0][1] for c in moved_once + moved_twice},
        "inventory_week_last": week_last, "inventory_week_avg": round(week_avg, 2),
        "inventory_week_sum": week_sum,
        "cancel_share": round(sum(o[2] == "cancelled" for o in orders) / total, 6),
        "cancel_share_avg_of_days": round(sum(shares) / len(shares), 6),
        "recycled_orders_new_person": by_person_orders.get("%db" % RECYCLED, 0),
        "recycled_orders_old_person": by_person_orders.get("%d" % RECYCLED, 0),
        "recycled_revenue_old_person": by_person_revenue.get("%d" % RECYCLED, 0),
        "late_orders": sum(1 for o in orders if o[1] == LATE and o[3].date() == LATE_ORDER_DAY),
        "late_revenue": sum(amount.get(o[0], 0) for o in orders if o[1] == LATE and o[3].date() == LATE_ORDER_DAY),
        "late_region": history[LATE][0][1],
    }


def connect():
    return psycopg2.connect(os.environ["STAND_DWH_DSN"])


def reset():
    history, products, orders, items, inventory = build()
    with connect() as c, c.cursor() as cur:
        cur.execute("DROP SCHEMA IF EXISTS app CASCADE; CREATE SCHEMA app;")
        cur.execute("""CREATE TABLE app.customers (id int PRIMARY KEY, name text, region text NOT NULL,
                         updated_at timestamp NOT NULL);
                       CREATE TABLE app.products (id int PRIMARY KEY, category text NOT NULL, price int NOT NULL);
                       CREATE TABLE app.orders (id bigint PRIMARY KEY, customer_id int NOT NULL, status text NOT NULL,
                         created_at timestamp NOT NULL);
                       CREATE TABLE app.order_items (id bigint PRIMARY KEY, order_id bigint NOT NULL,
                         product_id int NOT NULL, qty int NOT NULL, price int NOT NULL);
                       CREATE TABLE app.inventory (day date, product_id int, stock int, PRIMARY KEY (day, product_id))""")
        cur.executemany("INSERT INTO app.products VALUES (%s,%s,%s)", [(p, v[0], v[1]) for p, v in products.items()])
        cur.executemany("INSERT INTO app.orders VALUES (%s,%s,%s,%s)", orders)
        cur.executemany("INSERT INTO app.order_items VALUES (%s,%s,%s,%s,%s)", items)
        cur.executemany("INSERT INTO app.inventory VALUES (%s,%s,%s)", inventory)
    return len(orders), len(items)


def customers_at(day):
    history = build()[0]
    with connect() as c, c.cursor() as cur:
        cur.execute("TRUNCATE app.customers")
        cur.executemany("INSERT INTO app.customers VALUES (%s,%s,%s,%s)",
                        [(cid, name, region, upd) for cid, (region, upd, name) in sorted(state(history, day).items())])


def main():
    cmd = sys.argv[1]
    if cmd == "reset":
        print(json.dumps(dict(zip(("orders", "items"), reset()))))
    elif cmd == "customers-at":
        customers_at(dt.date.fromisoformat(sys.argv[2]))
    elif cmd == "truth":
        print(json.dumps(truth(), ensure_ascii=False))


if __name__ == "__main__":
    main()
