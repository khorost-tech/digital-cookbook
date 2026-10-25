"""Ось 4: backfill истории. Два DAG с одинаковой работой — выгрузка дня в raw
(delete-insert) и витрина dbt — и разной зависимостью от прошлого:
bf_free без depends_on_past, bf_ordered с ним. conf {"fail_day": "2026-09-05"}
роняет выгрузку этого дня.
"""

import datetime as dt
import subprocess

from airflow.sdk import dag, get_current_context, task

from pipeline import load as L


def make(dag_id, depends_on_past):
    @dag(dag_id=dag_id, schedule="@daily", start_date=dt.datetime(2026, 9, 1), end_date=dt.datetime(2026, 9, 14),
         catchup=False, max_active_runs=1, default_args={"retries": 0, "depends_on_past": depends_on_past})
    def pipeline():
        @task
        def extract_load():
            ctx = get_current_context()
            day = ctx["data_interval_start"].date()
            if (ctx["dag_run"].conf or {}).get("fail_day") == day.isoformat():
                raise RuntimeError("сбой выгрузки за %s" % day)
            orders, items = L.extract(day)
            L.load(day, orders, items, "delete-insert")

        @task
        def transform():
            subprocess.run(["/opt/dbt/bin/dbt", "run", "--project-dir", "/opt/stand/dbt"], check=True)

        extract_load() >> transform()

    return pipeline()


make("bf_free", depends_on_past=False)
make("bf_ordered", depends_on_past=True)
