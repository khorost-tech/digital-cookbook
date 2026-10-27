"""Ось 2: гранулярность факта и двойной счёт.

  category-item   — выручка по категории из факта позиции (контроль);
  category-order  — то же из факта заказа, соединённого с позициями: сумма заказа
                    повторяется на каждую его позицию;
  orders-distinct — число заказов count(DISTINCT order_id) по факту позиции (контроль);
  orders-count    — число заказов count(*) по факту позиции.

    python3 scripts/run-grain.py                 → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, GRAIN  # noqa: E402

QUERIES = {
    "category-item": "SELECT p.category, sum(i.amount) FROM marts.fct_order_item i "
                     "JOIN marts.dim_product p ON p.product_id = i.product_id GROUP BY 1",
    "category-order": "SELECT p.category, sum(o.amount) FROM marts.fct_order o "
                      "JOIN marts.fct_order_item i ON i.order_id = o.order_id "
                      "JOIN marts.dim_product p ON p.product_id = i.product_id GROUP BY 1",
    "orders-distinct": "SELECT 'orders', count(DISTINCT order_id) FROM marts.fct_order_item",
    "orders-count": "SELECT 'orders', count(*) FROM marts.fct_order_item",
}


def measure(name, truth):
    got = stack.kv(QUERIES[name])
    want = truth["revenue_by_category"] if name.startswith("category") else {"orders": truth["orders"]}
    g, w = sum(got.values()), sum(want.values())
    r = {"probe": "fct grain", "query": QUERIES[name], "got_total": g, "want_total": w,
         "ratio": round(g / w, 3), "misattributed": common.deviation(got, want)}
    r["outcome"] = "match" if got == {k: v for k, v in want.items()} else "mismatch"
    r["detail"] = "итог %d против эталона %d (×%.3f)" % (g, w, r["ratio"])
    return r


if __name__ == "__main__":
    common.run("grain", measure, list(QUERIES), GRAIN, END_MARKER)
