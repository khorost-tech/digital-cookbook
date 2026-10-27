"""Ось 4: ключи — измерение, пришедшее позже факта, и переиспользованный id.

Клиент 201 сделал 5 заказов 10.09, в источнике появился 11.09: у этих заказов
нет версии клиента на момент заказа.
  late-inner    — выручка, соединённая с версией клиента внутренним соединением;
  late-unknown  — левое соединение, без версии — «неизвестный участник»;
  late-reassign — без версии на момент заказа — первая версия клиента.
Итог сверяется с выручкой факта без соединений (контроль), регион позднего
клиента — с эталоном.
Клиент 77: 07.09 его id получил новый человек.
  recycled-scd1 — заказы «клиента 77» по бизнес-ключу (текущее имя);
  recycled-scd2 — заказы, у которых версия на момент заказа — нового владельца.

    python3 scripts/run-keys.py                  → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, KEYS  # noqa: E402

LATE = 201
JOINS = {
    "late-inner": "FROM marts.fct_order f JOIN marts.dim_customer_scd2 d ON d.customer_sk = f.customer_sk",
    "late-unknown": "FROM marts.fct_order f LEFT JOIN marts.dim_customer_scd2 d ON d.customer_sk = f.customer_sk",
    "late-reassign": "FROM marts.fct_order f LEFT JOIN marts.dim_customer_scd2 d ON d.customer_sk = coalesce("
                     "f.customer_sk, (SELECT customer_sk FROM marts.dim_customer_scd2 x WHERE x.customer_id = "
                     "f.customer_id ORDER BY valid_from LIMIT 1))",
}
RECYCLED = {
    "recycled-scd1": "SELECT count(*) FROM marts.fct_order f JOIN marts.dim_customer_scd1 d "
                     "ON d.customer_id = f.customer_id WHERE d.name = 'customer-77b'",
    "recycled-scd2": "SELECT count(*) FROM marts.fct_order f JOIN marts.dim_customer_scd2 d "
                     "ON d.customer_sk = f.customer_sk WHERE d.name = 'customer-77b'",
}


def measure(name, truth):
    if name in RECYCLED:
        got = int(stack.sql(RECYCLED[name])[0][0])
        want = truth["recycled_orders_new_person"]
        r = {"probe": "customer 77", "query": RECYCLED[name], "got_orders": got, "want_orders": want,
             "old_person_orders": truth["recycled_orders_old_person"]}
        r["outcome"] = "match" if got == want else "mismatch"
        r["detail"] = "новому владельцу id 77 приписано заказов %d, его собственных %d" % (got, want)
        return r
    total = int(stack.sql("SELECT sum(amount) FROM marts.fct_order")[0][0])
    got_total = int(stack.sql("SELECT sum(f.amount) " + JOINS[name])[0][0])
    late = stack.sql("SELECT coalesce(d.region, 'unknown'), count(*), sum(f.amount) " + JOINS[name] +
                     " WHERE f.customer_id = %d AND f.order_day = '2026-09-10' GROUP BY 1" % LATE)
    regions = sorted(l[0] for l in late)
    late_orders = sum(int(l[1]) for l in late)
    r = {"probe": "customer 201", "query": JOINS[name], "fact_total": total, "got_total": got_total,
         "missing": total - got_total, "late_orders_kept": late_orders, "late_regions": regions,
         "late_region_ok": regions == [truth["late_region"]]}
    r["outcome"] = "match" if r["missing"] == 0 and r["late_region_ok"] else "mismatch"
    r["detail"] = "потеряно выручки %d; заказов позднего клиента в отчёте %d из %d, регион %s" % (
        r["missing"], late_orders, truth["late_orders"], "/".join(regions) or "—")
    return r


if __name__ == "__main__":
    common.run("keys", measure, list(JOINS) + list(RECYCLED), KEYS, END_MARKER)
