"""ETL с подробными фактами: выгрузить день из источника, преобразовать в Python
(сумма и число позиций на заказ) и записать в хранилище очищенный факт заказа —
со статусом, без сырых строк. Выручку по правилу считает SQL в хранилище.

Третий вариант оси ETL/ELT: преобразование до загрузки, как у etl_daily, но
детализация и статус сохранены.
"""

import datetime as dt

from airflow.sdk import dag, get_current_context, task

from pipeline import load as L


@dag(dag_id="etl_facts_daily", schedule="@daily", start_date=dt.datetime(2026, 9, 1), catchup=False,
     max_active_runs=1, default_args={"retries": 0})
def etl_facts_daily():
    @task
    def extract_transform_load():
        day = get_current_context()["data_interval_start"].date()
        orders, items = L.extract(day)
        amount, count = {}, {}
        for _, oid, _, qty, price in items:
            amount[oid] = amount.get(oid, 0) + qty * price
            count[oid] = count.get(oid, 0) + 1
        with L.dwh() as c, c.cursor() as cur:
            cur.execute("CREATE SCHEMA IF NOT EXISTS marts_etl_facts; CREATE TABLE IF NOT EXISTS "
                        "marts_etl_facts.fct_order (order_id bigint PRIMARY KEY, order_day date, status text, "
                        "amount bigint, items int)")
            cur.execute("DELETE FROM marts_etl_facts.fct_order WHERE order_day = %s", (day,))
            cur.executemany("INSERT INTO marts_etl_facts.fct_order VALUES (%s, %s, %s, %s, %s)",
                            [(o[0], day, o[2], amount.get(o[0], 0), count.get(o[0], 0)) for o in orders])

    extract_transform_load()


etl_facts_daily()
