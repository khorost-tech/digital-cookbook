"""ETL: выгрузить день из источника, посчитать выручку в Python по правилу и
записать в хранилище только итог. Сырые строки в хранилище не попадают.

conf: {"rule": "v1"}.
"""

import datetime as dt

from airflow.sdk import dag, get_current_context, task

from pipeline import load as L

EXCLUDED = {"v1": {"cancelled"}, "v2": {"cancelled", "refunded"}}


@dag(dag_id="etl_daily", schedule="@daily", start_date=dt.datetime(2026, 9, 1), catchup=False,
     max_active_runs=1, default_args={"retries": 0})
def etl_daily():
    @task
    def extract_transform_load():
        ctx = get_current_context()
        rule = (ctx["dag_run"].conf or {}).get("rule", "v1")
        day = ctx["data_interval_start"].date()
        orders, items = L.extract(day)
        amount, count = {}, {}
        for _, oid, _, qty, price in items:
            amount[oid] = amount.get(oid, 0) + qty * price
            count[oid] = count.get(oid, 0) + 1
        revenue = sum(amount.get(o[0], 0) for o in orders if o[2] not in EXCLUDED[rule])
        with L.dwh() as c, c.cursor() as cur:
            cur.execute("CREATE SCHEMA IF NOT EXISTS marts_etl; CREATE TABLE IF NOT EXISTS marts_etl.daily_revenue "
                        "(order_day date PRIMARY KEY, revenue bigint, orders int, items int)")
            cur.execute("DELETE FROM marts_etl.daily_revenue WHERE order_day = %s", (day,))
            cur.execute("INSERT INTO marts_etl.daily_revenue VALUES (%s, %s, %s, %s)",
                        (day, revenue, len(orders), sum(count.values())))

    extract_transform_load()


etl_daily()
