"""Ось 3: аддитивность мер.

Остаток на складе — полуаддитивная мера: по товарам складывается, по времени —
нет. Неделя 08–14.09:
  stock-last — остаток последнего дня недели (контроль);
  stock-avg  — средний остаток за неделю;
  stock-sum  — сумма дневных остатков за неделю.
Доля отменённых — неаддитивная мера:
  cancel-ratio — отменённых заказов / всех за период (контроль);
  cancel-avg   — среднее дневных долей.

    python3 scripts/run-additivity.py            → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import ADDITIVITY, END_MARKER  # noqa: E402

WEEK = "day BETWEEN '2026-09-08' AND '2026-09-14'"
QUERIES = {
    "stock-last": "SELECT sum(stock) FROM marts.fct_inventory WHERE day = '2026-09-14'",
    "stock-avg": "SELECT round(sum(stock)::numeric / 7, 2) FROM marts.fct_inventory WHERE " + WEEK,
    "stock-sum": "SELECT sum(stock) FROM marts.fct_inventory WHERE " + WEEK,
    "cancel-ratio": "SELECT round(avg((status = 'cancelled')::int), 6) FROM marts.fct_order",
    "cancel-avg": "SELECT round(avg(s), 6) FROM (SELECT avg((status = 'cancelled')::int) AS s "
                  "FROM marts.fct_order GROUP BY order_day) d",
}
# с чем сверять: остатки — с остатком последнего дня (что спрашивают «сколько на складе»),
# среднее — со средним; доли — с долей за период
WANT = {"stock-last": "inventory_week_last", "stock-avg": "inventory_week_avg", "stock-sum": "inventory_week_last",
        "cancel-ratio": "cancel_share", "cancel-avg": "cancel_share"}


def measure(name, truth):
    got = float(stack.sql(QUERIES[name])[0][0])
    want = float(truth[WANT[name]])
    r = {"probe": "week 08-14.09" if name.startswith("stock") else "period", "query": QUERIES[name],
         "got": got, "want": want, "want_key": WANT[name], "ratio": round(got / want, 4)}
    r["outcome"] = "match" if abs(got - want) < 1e-6 else "mismatch"
    r["detail"] = "%s против эталона %s (×%.4f)" % (got, want, r["ratio"])
    return r


if __name__ == "__main__":
    common.run("additivity", measure, list(QUERIES), ADDITIVITY, END_MARKER)
