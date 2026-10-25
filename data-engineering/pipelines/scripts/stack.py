"""Единственное место, где стенд трогает Docker: compose, Airflow, dbt, SQL.

Стек — compose.yml (PostgreSQL + Airflow 3 с dbt). Запуски DAG — `airflow dags
test` с явной логической датой, backfill — `airflow backfill create`; никаких
ожиданий реального расписания. Эталон — из генератора (generator/gen.py), не из
хранилища.
"""

import json
import os
import re
import subprocess
import time

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DAYS = ["2026-09-%02d" % d for d in range(1, 15)]


def sh(*argv, check=True, timeout=900, input=None):
    p = subprocess.run(argv, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout,
                       input=input.encode() if input else None)
    out = p.stdout.decode("utf-8", "replace")
    if check and p.returncode != 0:
        raise SystemExit("%s: rc=%d\n%s" % (" ".join(argv), p.returncode, out[-3000:]))
    return p.returncode, out


def compose(*args, **kw):
    return sh("docker", "compose", *args, **kw)


def up(fresh=False):
    if fresh:
        compose("down", "-v", check=False)
    for attempt in range(3):
        rc, out = compose("up", "-d", "--build", "--wait", check=False, timeout=1800)
        if rc == 0:
            break
    else:
        raise SystemExit("стек не поднялся:\n" + out[-2000:])
    for _ in range(60):  # база метаданных Airflow мигрирована — CLI отвечает
        rc, _ = compose("exec", "-T", "airflow", "airflow", "dags", "list", check=False)
        if rc == 0:
            return
        time.sleep(3)
    raise SystemExit("Airflow не ответил")


def ax(*args, check=True, timeout=900):
    return compose("exec", "-T", "airflow", *args, check=check, timeout=timeout)


def sql(db, query):
    """Строки результата как списки строк (psql -A -t, разделитель |)."""
    _, out = compose("exec", "-T", "postgres", "psql", "-U", "postgres", "-d", db, "-v", "ON_ERROR_STOP=1",
                     "-AtF", "|", "-c", query)
    return [l.split("|") for l in out.strip().split("\n") if l]


def reset_data():
    """Источник заново, хранилище пусто, история запусков DAG стёрта."""
    ax("python", "-m", "generator.gen", "reset")
    sql("dwh", "DROP SCHEMA IF EXISTS raw CASCADE; DROP SCHEMA IF EXISTS staging CASCADE; "
               "DROP SCHEMA IF EXISTS marts CASCADE; DROP SCHEMA IF EXISTS marts_etl CASCADE; "
               "DROP SCHEMA IF EXISTS marts_etl_facts CASCADE; "
               "DROP SCHEMA IF EXISTS audit CASCADE")
    sql("airflow", "DELETE FROM task_instance; DELETE FROM backfill_dag_run; DELETE FROM dag_run; "
                   "DELETE FROM backfill")


def truth(rule="v1", late=None):
    args = ["python", "-m", "generator.gen", "truth", rule] + (list(late) if late else [])
    _, out = ax(*[str(a) for a in args])
    return json.loads(out.strip().split("\n")[-1])


def dag_test(dag, day, conf=None):
    """Один запуск DAG на логическую дату → состояние запуска (success | failed)."""
    args = ["airflow", "dags", "test", dag, day]
    if conf:
        args += ["--conf", json.dumps(conf)]
    _, out = ax(*args, check=False)
    m = re.findall(r"DagRun Finished: .*?state=(\w+)", out)
    if not m:
        raise SystemExit("нет итога запуска %s %s:\n%s" % (dag, day, out[-2000:]))
    return m[-1]


def dbt(*args, check=True):
    return ax("/opt/dbt/bin/dbt", *args, "--project-dir", "/opt/stand/dbt", check=check)


def revenue(table="marts.daily_revenue"):
    """{день: {revenue, orders, items}} из витрины хранилища."""
    rows = sql("dwh", "SELECT order_day, revenue, orders, items FROM %s ORDER BY 1" % table)
    return {r[0]: {"revenue": int(r[1]), "orders": int(r[2]), "items": int(r[3])} for r in rows}


def compare(got, want, days=DAYS, keys=("revenue",)):
    """Сверка с эталоном: совпавшие дни и расхождения по дням."""
    bad = {d: {"got": {k: got.get(d, {}).get(k) for k in keys}, "want": {k: want[d][k] for k in keys}}
           for d in days if any(got.get(d, {}).get(k) != want[d][k] for k in keys)}
    return len(days) - len(bad), bad


def airflow_sql(query):
    return sql("airflow", query)


def unpause(dag):
    ax("airflow", "dags", "unpause", dag)


def pause(dag):
    ax("airflow", "dags", "pause", dag, check=False)


def wait_runs(dag, run_type=None, timeout=900, stall=90):
    """Ждать, пока у DAG не останется запусков queued/running — или пока они не
    застрянут: ни одна задача не меняла состояние stall секунд (depends_on_past
    после сбоя держит следующие дни навсегда). → застряли ли запуски."""
    cond = " AND run_type = '%s'" % run_type if run_type else ""
    last, still = None, 0
    deadline = time.time() + timeout
    while time.time() < deadline:
        active = int(airflow_sql("SELECT count(*) FROM dag_run WHERE dag_id = '%s' AND state IN "
                                 "('queued', 'running')%s" % (dag, cond))[0][0])
        if not active:
            return False
        snap = airflow_sql("SELECT state, count(*) FROM task_instance WHERE dag_id = '%s' GROUP BY 1 ORDER BY 1" % dag)
        still = still + 5 if snap == last else 0
        last = snap
        if still >= stall:
            return True
        time.sleep(5)
    raise SystemExit("запуски %s не завершились за %d с" % (dag, timeout))
