"""Ось 3: какой день обрабатывает задача — «вчера» по часам или интервал запуска.

conf: {"how": "now", "wall": "2026-09-15"} — день = текущая дата минус сутки (так
часто пишут в cron); wall — «текущая дата» опыта: без неё результат зависел бы
от дня, когда запущен стенд;
{"how": "interval"} — день = data_interval_start запуска. Задача только пишет в
журнал dwh.audit.processed, какой день она взяла для какой логической даты.
"""

import datetime as dt

from airflow.sdk import dag, get_current_context, task

from pipeline import load as L


@dag(dag_id="interval_demo", schedule="@daily", start_date=dt.datetime(2026, 9, 1), end_date=dt.datetime(2026, 9, 14),
     catchup=False,
     max_active_runs=1, default_args={"retries": 0})
def interval_demo():
    @task
    def pick_day():
        ctx = get_current_context()
        how = (ctx["dag_run"].conf or {}).get("how", "interval")
        conf = ctx["dag_run"].conf or {}
        today = dt.date.fromisoformat(conf["wall"]) if conf.get("wall") else dt.date.today()
        day = today - dt.timedelta(days=1) if how == "now" else ctx["data_interval_start"].date()
        with L.dwh() as c, c.cursor() as cur:
            cur.execute("CREATE SCHEMA IF NOT EXISTS audit; CREATE TABLE IF NOT EXISTS audit.processed "
                        "(how text, logical_date date, day date, run_after date)")
            cur.execute("INSERT INTO audit.processed VALUES (%s, %s, %s, %s)",
                        (how, ctx["logical_date"].date(), day, ctx["dag_run"].run_after.date()))

    pick_day()


interval_demo()
