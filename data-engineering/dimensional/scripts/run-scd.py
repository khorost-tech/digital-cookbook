"""Ось 1: версии атрибута измерения — SCD1, SCD2, SCD3 на переездах клиентов.

Отчёты (без позднего клиента 201 — он на оси 4):
  asof-scd1 / asof-scd3 / asof-scd2 — выручка по региону на момент заказа через
      текущий регион (SCD1), текущий регион SCD3 и версию SCD2 на момент заказа;
  current-scd1 / current-scd2 — выручка по текущему региону человека через
      бизнес-ключ (текущая версия);
  first-scd3 — регион до первого переезда у переехавших: previous_region SCD3.
Счёт двумя величинами: отклонение итогов по регионам (половина суммы модулей
разностей с эталоном — встречные переносы между регионами в нём гасятся) и
выручка заказов, приписанных не тому региону, по каждому заказу. Для
first-scd3 — у скольких клиентов регион неверен.

    python3 scripts/run-scd.py                 → все варианты, фикстура
    python3 scripts/run-scd.py asof-scd1       → один вариант
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, SCD  # noqa: E402

LATE = 201
QUERIES = {
    "asof-scd1": "SELECT d.region, sum(f.amount) FROM marts.fct_order f JOIN marts.dim_customer_scd1 d "
                 "ON d.customer_id = f.customer_id WHERE f.customer_id <> %d GROUP BY 1" % LATE,
    "asof-scd3": "SELECT d.current_region, sum(f.amount) FROM marts.fct_order f JOIN marts.dim_customer_scd3 d "
                 "ON d.customer_id = f.customer_id WHERE f.customer_id <> %d GROUP BY 1" % LATE,
    "asof-scd2": "SELECT d.region, sum(f.amount) FROM marts.fct_order f JOIN marts.dim_customer_scd2 d "
                 "ON d.customer_sk = f.customer_sk WHERE f.customer_id <> %d GROUP BY 1" % LATE,
    "current-scd1": "SELECT d.region, sum(f.amount) FROM marts.fct_order f JOIN marts.dim_customer_scd1 d "
                    "ON d.customer_id = f.customer_id WHERE f.customer_id <> %d GROUP BY 1" % LATE,
    "current-scd2": "SELECT d.region, sum(f.amount) FROM marts.fct_order f JOIN marts.dim_customer_scd2 d "
                    "ON d.customer_id = f.customer_id AND d.valid_to = '9999-12-31' "
                    "WHERE f.customer_id <> %d GROUP BY 1" % LATE,
}
VARIANTS = list(QUERIES) + ["first-scd3"]


def want_regions(truth, key):
    want = dict(truth[key])
    want[truth["late_region"]] -= truth["late_revenue"]  # поздний клиент — на оси 4
    return want


def measure(name, truth):
    r = {"probe": "regions"}
    if name == "first-scd3":
        prev = dict(stack.sql("SELECT customer_id::text, previous_region FROM marts.dim_customer_scd3 "
                              "WHERE previous_region IS NOT NULL"))
        once = [str(c) for c in truth["moved_once"]]
        twice = [str(c) for c in truth["moved_twice"]]
        wrong_once = sum(prev.get(c) != truth["first_region"][c] for c in once)
        wrong_twice = sum(prev.get(c) != truth["first_region"][c] for c in twice)
        r.update({"moved_once": len(once), "moved_twice": len(twice), "wrong_once": wrong_once,
                  "wrong_twice": wrong_twice})
        r["outcome"] = "match" if wrong_once + wrong_twice == 0 else "mismatch"
        r["detail"] = "регион до переезда неверен: у переехавших раз %d из %d, дважды %d из %d" % (
            wrong_once, len(once), wrong_twice, len(twice))
        return r
    got = stack.kv(QUERIES[name])
    asof = name.startswith("asof")
    want = want_regions(truth, "revenue_by_region_asof" if asof else "revenue_by_region_current")
    dev = common.deviation(got, want)
    # Тот же отчёт по заказам: регион каждого заказа против эталона генератора.
    per_order = QUERIES[name].replace("sum(f.amount)", "f.order_id, f.amount").replace(" GROUP BY 1", "")
    order_truth = truth["order_region_asof" if asof else "order_region_current"]
    wrong = [(o, int(float(a))) for reg, o, a in stack.sql(per_order)
             if o in order_truth and order_truth[o] != reg]
    wrong_amount = sum(a for _, a in wrong)
    r.update({"query": QUERIES[name], "misattributed": dev, "total": sum(want.values()),
              "got_total": sum(got.values()), "wrong_orders": len(wrong), "wrong_amount": wrong_amount})
    r["outcome"] = "match" if dev == 0 and wrong_amount == 0 and r["got_total"] == r["total"] else "mismatch"
    r["detail"] = "отклонение итогов по регионам %d из %d (%.2f%%); заказов не в том регионе %d на %d (%.2f%%)" % (
        dev, r["total"], 100.0 * dev / r["total"], len(wrong), wrong_amount, 100.0 * wrong_amount / r["total"])
    return r


if __name__ == "__main__":
    common.run("scd", measure, VARIANTS, SCD, END_MARKER)
