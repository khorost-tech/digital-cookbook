"""ELT: выгрузить день из источника в dwh.raw как есть, затем dbt строит витрину.

Режим загрузки и прочее — из conf запуска: {"mode": "delete-insert", "rule": "v1",
"crash_after": null, "by": "created_at" | "ingested_at", "lookback": 0,
"models": "+daily_revenue"}. lookback — сколько прошлых дней перезагрузить вместе
с текущим (окно пересмотра для поздних данных).
"""

import datetime as dt
import subprocess

from airflow.sdk import dag, get_current_context, task

from pipeline import load as L


@dag(dag_id="elt_daily", schedule="@daily", start_date=dt.datetime(2026, 9, 1), catchup=False,
     max_active_runs=1, default_args={"retries": 0})
def elt_daily():
    @task
    def extract_load():
        ctx = get_current_context()
        conf = ctx["dag_run"].conf or {}
        day = ctx["data_interval_start"].date()
        for back in range(conf.get("lookback", 0), -1, -1):
            d = day - dt.timedelta(days=back)
            orders, items = L.extract(d, by=conf.get("by", "created_at"))
            L.load(d, orders, items, conf.get("mode", "delete-insert"), conf.get("crash_after"))

    @task
    def transform():
        conf = get_current_context()["dag_run"].conf or {}
        subprocess.run(["/opt/dbt/bin/dbt", "run", "--project-dir", "/opt/stand/dbt", "--select",
                        conf.get("models", "+daily_revenue"), "--vars",
                        '{"rule": "%s", "lookback_days": %d}' % (conf.get("rule", "v1"), conf.get("mart_lookback", 0))],
                       check=True)

    extract_load() >> transform()


elt_daily()
