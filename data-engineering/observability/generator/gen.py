"""Генератор 28 дней загрузок заказов с недельной сезонностью и подсаженными
инцидентами; эталон инцидентов — его же, не мониторов.

Дни 18.08–14.09.2026. Будни — 280–320 заказов, выходные — 160–200. Загрузка
дня приходит в 02:00 следующего дня. Инциденты:
  volume       02.09 (ср) — пришло 62% обычного: объём будня внутри нормы выходных;
  freshness    08.09 (вт) — загрузка опоздала на 30 часов;
  schema       10.09 (чт) — источник добавил колонку coupon;
  distribution 12.09 (сб) — треть сумм в рублях вместо копеек.

    python3 -m generator.gen reset                       пустые raw.orders и raw.customers
    python3 -m generator.gen load DAY [SCENARIO]         загрузка дня DAY в raw.orders
    python3 -m generator.gen incidents [SCENARIO]        эталон инцидентов (JSON)
SCENARIO: base (по умолчанию) — четыре инцидента выше; drift — длительная
деградация: с 02.09 каждый день треть сумм в рублях, других инцидентов нет.
"""

import datetime as dt
import json
import os
import random
import sys

import psycopg2

SEED = 20260818
DAYS = [dt.date(2026, 8, 18) + dt.timedelta(days=i) for i in range(28)]
INCIDENTS = {"2026-09-02": "volume", "2026-09-08": "freshness", "2026-09-10": "schema", "2026-09-12": "distribution"}
SCENARIOS = {"base": INCIDENTS,
             "drift": {d.isoformat(): "distribution" for d in DAYS if d >= dt.date(2026, 9, 2)}}
CUSTOMERS = 200


def day_rows(day, incidents=INCIDENTS):
    rnd = random.Random(SEED + day.toordinal())
    weekend = day.weekday() >= 5
    n = rnd.randint(160, 200) if weekend else rnd.randint(280, 320)
    if incidents.get(day.isoformat()) == "volume":
        n = int(n * 0.62)
    base = (day.toordinal() - DAYS[0].toordinal()) * 1000
    rows = []
    for i in range(n):
        amount = rnd.randint(1, 40) * 500
        if incidents.get(day.isoformat()) == "distribution" and i % 3 == 0:
            amount = max(1, amount // 100)
        rows.append((base + i + 1, rnd.randint(1, CUSTOMERS), rnd.choices(["paid", "cancelled", "refunded"],
                                                                           [85, 10, 5])[0], amount,
                     dt.datetime.combine(day, dt.time()) + dt.timedelta(seconds=rnd.randint(0, 86399))))
    return rows


def loaded_at(day, incidents=INCIDENTS):
    late = 30 if incidents.get(day.isoformat()) == "freshness" else 0
    return dt.datetime.combine(day + dt.timedelta(days=1), dt.time(2, 0)) + dt.timedelta(hours=late)


def connect():
    return psycopg2.connect(os.environ["STAND_DWH_DSN"])


def reset():
    with connect() as c, c.cursor() as cur:
        cur.execute("DROP SCHEMA IF EXISTS raw CASCADE; CREATE SCHEMA raw;")
        cur.execute("CREATE TABLE raw.customers (id int PRIMARY KEY, region text)")
        rnd = random.Random(SEED)
        cur.executemany("INSERT INTO raw.customers VALUES (%s, %s)",
                        [(i, rnd.choice(["north", "south", "east", "west", "center"])) for i in range(1, CUSTOMERS + 1)])
        cur.execute("CREATE TABLE raw.orders (id bigint, customer_id int, status text, amount bigint, "
                    "created_at timestamp, loaded_at timestamp)")


def load(day, incidents=INCIDENTS):
    with connect() as c, c.cursor() as cur:
        if incidents.get(day.isoformat()) == "schema":
            cur.execute("ALTER TABLE raw.orders ADD COLUMN IF NOT EXISTS coupon text")
        cur.executemany("INSERT INTO raw.orders (id, customer_id, status, amount, created_at, loaded_at) "
                        "VALUES (%s,%s,%s,%s,%s,%s)", [r + (loaded_at(day, incidents),) for r in day_rows(day, incidents)])


def main():
    cmd = sys.argv[1]
    if cmd == "reset":
        reset()
    elif cmd == "load":
        load(dt.date.fromisoformat(sys.argv[2]), SCENARIOS[sys.argv[3] if len(sys.argv) > 3 else "base"])
    elif cmd == "incidents":
        scenario = sys.argv[2] if len(sys.argv) > 2 else "base"
        print(json.dumps({"days": [d.isoformat() for d in DAYS], "incidents": SCENARIOS[scenario]}))


if __name__ == "__main__":
    main()
