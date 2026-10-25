"""Ось 6: повторы задачи Airflow.

conf: {"fail_times": k, "mode": "append" | "delete-insert"}. Задача грузит день
03.09 в raw и падает на попытках с номером ≤ k — после записи части строк
(crash_after=5 пакетов), как переходная ошибка посреди загрузки. retries задаёт
сам DAG: retry_r1, retry_r3.
"""

import datetime as dt

from airflow.sdk import dag, get_current_context, task

from pipeline import load as L


def make(dag_id, retries):
    @dag(dag_id=dag_id, schedule="@daily", start_date=dt.datetime(2026, 9, 1), end_date=dt.datetime(2026, 9, 14),
         catchup=False, max_active_runs=1,
         default_args={"retries": retries, "retry_delay": dt.timedelta(seconds=2)})
    def pipeline():
        @task
        def extract_load():
            ctx = get_current_context()
            conf = ctx["dag_run"].conf or {}
            # Ручной запуск в Airflow 3 получает data_interval_start = момент запуска,
            # а не логическую дату (ось 3) — день берётся из logical_date.
            day = ctx["logical_date"].date()
            orders, items = L.extract(day)
            crash = 5 if ctx["ti"].try_number <= conf.get("fail_times", 0) else None
            L.load(day, orders, items, conf.get("mode", "delete-insert"), crash_after=crash)

        extract_load()

    return pipeline()


make("retry_r1", 1)
make("retry_r3", 3)
