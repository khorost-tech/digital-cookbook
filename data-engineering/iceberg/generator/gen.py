"""Заказы для стенда iceberg: 14 дней сентября 2026, фиксированное зерно.

Плоская таблица заказа: id, customer_id, status, amount (копейки), created_at.
Те же дни и тот же порядок величин, что у стендов pipelines и dimensional.
"""

import datetime as dt
import random

SEED = 20260901
DAYS = [dt.date(2026, 9, 1) + dt.timedelta(days=i) for i in range(14)]


def orders(seed=SEED):
    rnd = random.Random(seed)
    out, oid = [], 0
    for day in DAYS:
        for _ in range(rnd.randint(250, 350)):
            oid += 1
            out.append({"id": oid, "customer_id": rnd.randint(1, 200),
                        "status": rnd.choices(["paid", "cancelled", "refunded"], [85, 10, 5])[0],
                        "amount": rnd.randint(1, 40) * 500,
                        "created_at": dt.datetime.combine(day, dt.time()) + dt.timedelta(seconds=rnd.randint(0, 86399))})
    return out


def by_day(rows):
    out = {}
    for r in rows:
        out.setdefault(r["created_at"].date().isoformat(), []).append(r)
    return out
