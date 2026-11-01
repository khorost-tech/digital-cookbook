"""Генератор партии заказов с подсаженным дефектом и эталоном плохих строк.

Партия — заказы одного дня (10.09.2026), фиксированное зерно; справочник —
клиенты 1–200. Дефект подсаживается в известные строки, и генератор знает их
id — проверка сверяется с ним, а не с собой. Время выгрузки (loaded_at)
ставится от текущего момента базы: «свежая» партия — час назад, устаревшая —
три дня назад; иначе свежесть зависела бы от даты прогона стенда.

    python3 -m generator.gen load DEFECT     партия в raw.orders (+ raw.customers)
    python3 -m generator.gen bad DEFECT      эталон: плохие id и масштаб дефекта (JSON)
DEFECT: clean | nulls | duplicates | range | enum | orphans | stale | shift | volume | mixed | units | nullfields
"""

import datetime as dt
import json
import os
import random
import sys

import psycopg2

SEED = 20260910
DAY = dt.date(2026, 9, 10)
CUSTOMERS = 200
DEFECTS = ("clean", "nulls", "duplicates", "range", "enum", "orphans", "stale", "shift", "volume", "mixed", "units",
           "nullfields")


def clean(seed=SEED, n=None):
    rnd = random.Random(seed)
    rows = []
    for i in range(1, (n or rnd.randint(280, 320)) + 1):
        rows.append({"id": i, "customer_id": rnd.randint(1, CUSTOMERS),
                     "status": rnd.choices(["paid", "cancelled", "refunded"], [85, 10, 5])[0],
                     "amount": rnd.randint(1, 40) * 500,
                     "created_at": dt.datetime.combine(DAY, dt.time()) + dt.timedelta(seconds=rnd.randint(0, 86399))})
    return rows


def batch(defect):
    """(строки, плохие id, сдвиг loaded_at в часах назад)."""
    rnd = random.Random(SEED + DEFECTS.index(defect))
    rows, bad, age = clean(), set(), 1
    pick = lambda k: sorted(rnd.sample(range(len(rows)), k))  # noqa: E731
    if defect == "nulls":
        for i in pick(15):
            rows[i]["customer_id"] = None
            bad.add(rows[i]["id"])
    elif defect == "duplicates":
        extra = [dict(rows[i]) for i in pick(10)]
        bad |= {r["id"] for r in extra}
        rows += extra
    elif defect == "range":
        for i in pick(12):
            rows[i]["amount"] = -rows[i]["amount"]
            bad.add(rows[i]["id"])
    elif defect == "enum":
        for i in pick(8):
            rows[i]["status"] = "shipped"
            bad.add(rows[i]["id"])
    elif defect == "orphans":
        for i in pick(9):
            rows[i]["customer_id"] = 9000 + i
            bad.add(rows[i]["id"])
    elif defect == "stale":
        age = 72
        bad = {r["id"] for r in rows}
    elif defect == "shift":
        for i in pick(len(rows) // 3):  # рубли вместо копеек: каждое значение допустимо
            rows[i]["amount"] = max(1, rows[i]["amount"] // 100)
            bad.add(rows[i]["id"])
    elif defect == "volume":
        rows = clean(n=30)
        bad = {r["id"] for r in rows}
    elif defect == "units":  # ось 3: источник перешёл на рубли во всей партии, тип тот же
        for r in rows:
            r["amount"] = max(1, r["amount"] // 100)
        bad = {r["id"] for r in rows}
    elif defect == "nullfields":  # ось 2: пустой статус и пустая сумма — трёхзначная логика флага
        for k, i in enumerate(pick(12)):
            rows[i]["status" if k % 2 == 0 else "amount"] = None
            bad.add(rows[i]["id"])
    elif defect == "mixed":  # ось 2: 5% плохих строк трёх видов
        for k, i in enumerate(pick(15)):
            if k % 3 == 0:
                rows[i]["customer_id"] = None
            elif k % 3 == 1:
                rows[i]["amount"] = -rows[i]["amount"]
            else:
                rows[i]["status"] = "shipped"
            bad.add(rows[i]["id"])
    return rows, bad, age


def connect():
    return psycopg2.connect(os.environ["STAND_DWH_DSN"])


def load(defect, amount_type="bigint", drop_amount=False):
    """Партия в raw.orders; amount_type/drop_amount — смена схемы источника (ось 3)."""
    rows, _, age = batch(defect)
    with connect() as c, c.cursor() as cur:
        cur.execute("CREATE SCHEMA IF NOT EXISTS raw; DROP TABLE IF EXISTS raw.orders CASCADE; "
                    "DROP TABLE IF EXISTS raw.customers CASCADE")
        cur.execute("CREATE TABLE raw.customers (id int PRIMARY KEY, name text)")
        cur.executemany("INSERT INTO raw.customers VALUES (%s, %s)", [(i, "c%d" % i) for i in range(1, CUSTOMERS + 1)])
        amount_col = "" if drop_amount else ", amount %s" % amount_type
        cur.execute("CREATE TABLE raw.orders (id bigint, customer_id int, status text%s, created_at timestamp, "
                    "loaded_at timestamptz)" % amount_col)
        for r in rows:
            vals = [r["id"], r["customer_id"], r["status"]] + ([] if drop_amount else [r["amount"]]) + [r["created_at"]]
            marks = ",".join(["%s"] * len(vals))
            cur.execute("INSERT INTO raw.orders VALUES (%s, now() - interval '%d hours')" % (marks, age), vals)
    return len(rows)


def main():
    cmd, defect = sys.argv[1], sys.argv[2]
    if cmd == "load":
        opts = sys.argv[3:]
        print(json.dumps({"rows": load(defect, amount_type="numeric(12,2)" if "numeric" in opts else "bigint",
                                       drop_amount="drop-amount" in opts)}))
    elif cmd == "bad":
        rows, bad, age = batch(defect)
        clean_rows = clean()
        print(json.dumps({"defect": defect, "rows": len(rows), "bad_ids": sorted(bad), "bad": len(bad),
                          "clean_rows": len(clean_rows),
                          "clean_mean": sum(r["amount"] for r in clean_rows) / len(clean_rows),
                          "mean": (lambda v: sum(v) / len(v))([r["amount"] for r in rows if r["amount"] is not None]),
                          "age_hours": age}))


if __name__ == "__main__":
    main()
