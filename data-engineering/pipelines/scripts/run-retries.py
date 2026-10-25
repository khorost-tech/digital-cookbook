"""Ось 6: повторы задачи Airflow.

DAG retry_r1 (retries=1) и retry_r3 (retries=3) грузят 03.09 в raw; задача падает
на попытках с номером ≤ fail_times — после записи 5 пакетов позиций. Запуск —
`airflow dags trigger` с логической датой, повторы делает планировщик. Счёт:
состояние задачи, число попыток (task_instance.try_number), дубли и потери в raw.

    python3 scripts/run-retries.py                → все варианты, фикстура
    python3 scripts/run-retries.py r3-append-k1   → один вариант
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import END_MARKER, RETRIES  # noqa: E402

FIXTURE = os.path.join(stack.HERE, "fixtures", "retries.jsonl")
DAY = "2026-09-03"
# вариант → (DAG, падений подряд, режим загрузки)
VARIANTS = {
    "control": ("retry_r3", 0, "delete-insert"),
    "r3-k2": ("retry_r3", 2, "delete-insert"),
    "r1-k2": ("retry_r1", 2, "delete-insert"),
    "r3-append-k1": ("retry_r3", 1, "append"),
    "r3-delete-insert-k1": ("retry_r3", 1, "delete-insert"),
}


def measure(name):
    dag, k, mode = VARIANTS[name]
    stack.reset_data()
    stack.unpause(dag)
    try:
        stack.ax("airflow", "dags", "trigger", dag, "--logical-date", DAY + "T00:00:00+00:00",
                 "--conf", json.dumps({"fail_times": k, "mode": mode}))
        stack.wait_runs(dag)
    finally:
        stack.pause(dag)
    state, tries = stack.airflow_sql("SELECT state, try_number FROM task_instance WHERE dag_id = '%s' "
                                     "AND task_id = 'extract_load'" % dag)[0]
    want = stack.truth("v1")[DAY]["items"]
    rows, distinct = (int(x) for x in stack.sql(
        "dwh", "SELECT count(*), count(DISTINCT id) FROM raw.order_items WHERE load_day = '%s'" % DAY)[0])
    retries = {"retry_r1": 1, "retry_r3": 3}[dag]
    r = {"kind": "retries", "variant": name, "probe": "%s fail%d %s" % (dag, k, mode),
         "cmd": "python3 scripts/run-retries.py %s" % name, "dag": dag, "retries": retries, "fail_times": k,
         "mode": mode, "task_state": state, "try_number": int(tries), "want_items": want, "rows": rows,
         "duplicates": rows - distinct, "missing": want - distinct}
    ok = state == "success" and r["duplicates"] == 0 and r["missing"] == 0
    r["outcome"] = "match" if ok else "mismatch"
    r["detail"] = "задача %s на попытке %d (retries=%d, падений %d); дублей %d, потерь %d" % (
        state, r["try_number"], retries, k, r["duplicates"], r["missing"])
    RETRIES.parse_line(json.dumps(r, ensure_ascii=False))
    return r


SHOW = ("variant", "outcome", "detail")


def main():
    stack.up()
    names = sys.argv[1:] or list(VARIANTS)
    rows = []
    for n in names:
        rows.append(measure(n))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    if not sys.argv[1:]:
        with open(FIXTURE, "w", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
            f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
