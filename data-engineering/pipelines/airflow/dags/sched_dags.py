"""Ось 3, обычный запуск планировщика: какой день достаётся задаче по
data_interval_start при расписании @daily.

sched_trigger — расписание Airflow 3 по умолчанию (create_cron_data_intervals =
False: срабатывание без интервала, начало интервала = момент срабатывания);
sched_interval — CronDataIntervalTimetable, как в Airflow 2: запуск в полночь
получает интервал за прошедшие сутки. Задача пишет в журнал dwh.audit.scheduled
момент срабатывания и взятый день.
"""

import datetime as dt

from airflow.sdk import dag, get_current_context, task
from airflow.timetables.interval import CronDataIntervalTimetable

from pipeline import load as L


def make(dag_id, schedule):
    @dag(dag_id=dag_id, schedule=schedule, start_date=dt.datetime(2026, 9, 1), catchup=False,
         max_active_runs=1, default_args={"retries": 0})
    def pipeline():
        @task
        def pick_day():
            ctx = get_current_context()
            run_after = ctx["dag_run"].run_after
            with L.dwh() as c, c.cursor() as cur:
                cur.execute("CREATE SCHEMA IF NOT EXISTS audit; CREATE TABLE IF NOT EXISTS audit.scheduled "
                            "(dag_id text, run_after timestamptz, interval_start timestamptz, "
                            "interval_end timestamptz, day date)")
                cur.execute("INSERT INTO audit.scheduled VALUES (%s, %s, %s, %s, %s)",
                            (dag_id, run_after, ctx["data_interval_start"], ctx["data_interval_end"],
                             ctx["data_interval_start"].date()))

        pick_day()

    return pipeline()


make("sched_trigger", "@daily")
make("sched_interval", CronDataIntervalTimetable("@daily", timezone="UTC"))
