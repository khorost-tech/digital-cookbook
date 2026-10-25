"""Ось 3: интервал данных против текущего времени.

DAG interval_demo запускается за 14 логических дат (как при backfill истории):
how=interval берёт день из data_interval_start, how=now — «вчера» по часам.
Часы опыта фиксированы (WALL — история пересчитывается 15.09, на следующий день
после последнего дня): иначе число зависело бы от даты прогона стенда. Счёт по журналу dwh.audit.processed: какие из 14 дней
обработаны ровно раз, больше раза, ни разу. Контроль — how=interval.

Ещё два варианта — обычный запуск планировщика при снятии паузы: DAG
sched_trigger (расписание Airflow 3 по умолчанию) и sched_interval
(CronDataIntervalTimetable). Публикуется не дата, а сдвиг: на сколько дней взятый
день отстоит от дня срабатывания, и длина интервала данных в часах.

    python3 scripts/run-interval.py               → все варианты, фикстура
    python3 scripts/run-interval.py now           → один вариант
"""

import collections
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import END_MARKER, INTERVAL  # noqa: E402

FIXTURE = os.path.join(stack.HERE, "fixtures", "interval.jsonl")
WALL = "2026-09-15"


def measure_scheduled(name):
    stack.reset_data()
    dag = "sched_" + name.split("-")[1]
    stack.unpause(dag)
    try:
        for _ in range(60):  # планировщик создаёт запуск за последний срок
            if int(stack.airflow_sql("SELECT count(*) FROM dag_run WHERE dag_id = '%s'" % dag)[0][0]):
                break
            stack.time.sleep(3)
        stack.wait_runs(dag)
    finally:
        stack.pause(dag)
    rows = stack.sql("dwh", "SELECT (day - run_after::date), extract(epoch FROM interval_end - interval_start) / 3600 "
                            "FROM audit.scheduled WHERE dag_id = '%s'" % dag)
    r = {"kind": "interval", "variant": name, "probe": dag, "cmd": "python3 scripts/run-interval.py %s" % name,
         "how": name, "dag": dag, "runs": len(rows)}
    if len(rows) != 1:
        r["outcome"], r["detail"] = "error", "запусков с журналом: %d" % len(rows)
    else:
        r["day_offset"], r["interval_hours"] = int(rows[0][0]), int(float(rows[0][1]))
        r["outcome"] = "match" if r["day_offset"] == -1 else "mismatch"
        r["detail"] = "взят день срабатывания %+d, интервал данных %d ч" % (r["day_offset"], r["interval_hours"])
    INTERVAL.parse_line(json.dumps(r, ensure_ascii=False))
    return r


def measure_manual(name):
    """Ручной запуск `airflow dags trigger --logical-date 2026-09-03`: какой день
    берёт задача по data_interval_start — логическую дату или день запуска."""
    stack.reset_data()
    stack.unpause("interval_demo")
    try:
        stack.ax("airflow", "dags", "trigger", "interval_demo", "--logical-date", "2026-09-03T00:00:00+00:00",
                 "--conf", json.dumps({"how": "interval"}))
        stack.wait_runs("interval_demo")
    finally:
        stack.pause("interval_demo")
    rows = stack.sql("dwh", "SELECT day = logical_date, day = run_after FROM audit.processed")
    r = {"kind": "interval", "variant": name, "probe": "interval_demo manual", "how": name,
         "cmd": "python3 scripts/run-interval.py %s" % name, "runs": len(rows)}
    if len(rows) != 1:
        r["outcome"], r["detail"] = "error", "запусков с журналом: %d" % len(rows)
    else:
        r["picked_logical"], r["picked_run_day"] = rows[0][0] == "t", rows[0][1] == "t"
        r["outcome"] = "match" if r["picked_logical"] else "mismatch"
        r["detail"] = "по data_interval_start взят %s" % (
            "день логической даты" if r["picked_logical"] else
            "день запуска, а не логической даты" if r["picked_run_day"] else "другой день")
    INTERVAL.parse_line(json.dumps(r, ensure_ascii=False))
    return r


def measure(how):
    if how.startswith("scheduled-"):
        return measure_scheduled(how)
    if how == "manual-trigger":
        return measure_manual(how)
    stack.reset_data()
    runs = [stack.dag_test("interval_demo", d, {"how": how, "wall": WALL}) for d in stack.DAYS]
    picked = [r[0] for r in stack.sql("dwh", "SELECT day FROM audit.processed WHERE how = '%s' "
                                             "ORDER BY logical_date" % how)]
    count = collections.Counter(picked)
    once = sum(1 for d in stack.DAYS if count[d] == 1)
    many = sum(1 for d in stack.DAYS if count[d] > 1)
    never = sum(1 for d in stack.DAYS if count[d] == 0)
    outside = sorted(set(picked) - set(stack.DAYS))
    r = {"kind": "interval", "variant": how, "probe": "14-logical-dates",
         "cmd": "python3 scripts/run-interval.py %s" % how, "how": how, "wall": WALL, "runs_ok": runs.count("success"),
         "days": len(stack.DAYS), "once": once, "many": many, "never": never,
         "distinct_picked": len(count), "outside_range": len(outside)}
    if r["runs_ok"] != len(stack.DAYS):
        r["outcome"], r["detail"] = "error", "упало запусков: %d" % (len(stack.DAYS) - r["runs_ok"])
    else:
        r["outcome"] = "match" if once == len(stack.DAYS) else "mismatch"
        r["detail"] = "из %d дней: ровно раз %d, больше раза %d, ни разу %d; разных дней взято %d, вне истории %d" % (
            len(stack.DAYS), once, many, never, len(count), len(outside))
    INTERVAL.parse_line(json.dumps(r, ensure_ascii=False))
    return r


SHOW = ("variant", "outcome", "detail")


def main():
    stack.up()
    hows = sys.argv[1:] or ["interval", "now", "scheduled-trigger", "scheduled-interval", "manual-trigger"]
    rows = [measure(h) for h in hows]
    for r in rows:
        print(json.dumps({k: r[k] for k in SHOW}, ensure_ascii=False), flush=True)
    if not sys.argv[1:]:
        with open(FIXTURE, "w", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
            f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
