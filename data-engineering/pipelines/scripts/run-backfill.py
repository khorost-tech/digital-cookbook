"""Ось 4: airflow backfill create за историю.

DAG bf_free (без depends_on_past) и bf_ordered (с ним) делают одно и то же:
выгрузка дня в raw (delete-insert) и витрина dbt. Варианты:
  full              — backfill 14 дней (контроль: витрина = эталон);
  repeat-none       — тот же backfill ещё раз, по умолчанию (--reprocess-behavior none);
  repeat-completed  — ещё раз с --reprocess-behavior completed;
  fail-free         — сбой выгрузки за 05.09, bf_free;
  ordered-default   — bf_ordered (depends_on_past): 03.09 уже обработан ручным
                      запуском, backfill без --reprocess-behavior;
  ordered-clean     — bf_ordered, ни одна дата не обработана, backfill без
                      --reprocess-behavior (проверка: отказ не зависит от истории);
  fail-ordered      — сбой за 05.09, bf_ordered, --reprocess-behavior failed;
  backwards-ordered — --run-backwards на bf_ordered.
Счёт — только запуски backfill (обычный запуск при снятии паузы — отдельно, не
публикуется: его дата зависит от сегодняшнего дня).

    python3 scripts/run-backfill.py                 → все варианты, фикстура
    python3 scripts/run-backfill.py fail-ordered    → один вариант
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import BACKFILL, END_MARKER  # noqa: E402

FIXTURE = os.path.join(stack.HERE, "fixtures", "backfill.jsonl")
FAIL_DAY = "2026-09-05"
VARIANTS = ("full", "repeat-none", "repeat-completed", "fail-free", "ordered-default", "ordered-clean",
            "fail-ordered", "backwards-ordered")
REFUSED = ("ordered-default", "ordered-clean", "backwards-ordered")


def backfill(dag, *extra, conf=None):
    args = ["airflow", "backfill", "create", "--dag-id", dag, "--from-date", stack.DAYS[0],
            "--to-date", stack.DAYS[-1], "--max-active-runs", "1", *extra]
    if conf:
        args += ["--dag-run-conf", json.dumps(conf)]
    rc, out = stack.ax(*args, check=False)
    return rc, out


def counts(dag, backfill_id):
    rows = stack.airflow_sql(
        "SELECT r.logical_date::date, ti.state FROM backfill_dag_run b JOIN dag_run r ON r.id = b.dag_run_id "
        "JOIN task_instance ti ON ti.run_id = r.run_id AND ti.dag_id = r.dag_id AND ti.task_id = 'extract_load' "
        "WHERE b.backfill_id = %d ORDER BY 1" % backfill_id)
    states = {d: (s or "none") for d, s in rows}
    return {"runs": len(states), "success": sum(s == "success" for s in states.values()),
            "failed": sum(s == "failed" for s in states.values()),
            "not_run": sum(s not in ("success", "failed") for s in states.values()),
            "first_failed": min((d for d, s in states.items() if s == "failed"), default=None)}


def last_backfill_id():
    return int(stack.airflow_sql("SELECT coalesce(max(id), 0) FROM backfill")[0][0])


def run_backfill(dag, *extra, conf=None):
    before = last_backfill_id()
    rc, out = backfill(dag, *extra, conf=conf)
    bid = last_backfill_id()
    if bid == before:
        return rc, out, None, False
    stuck = stack.wait_runs(dag, run_type="backfill")
    if not stuck:  # следующий backfill CLI примет, только когда этот помечен завершённым
        for _ in range(60):
            if stack.airflow_sql("SELECT completed_at IS NOT NULL FROM backfill WHERE id = %d" % bid)[0][0] == "t":
                break
            stack.time.sleep(3)
    return rc, out, bid, stuck


def measure(name):
    stack.reset_data()
    dag = "bf_ordered" if "ordered" in name else "bf_free"
    stack.unpause(dag)
    try:
        conf = {"fail_day": FAIL_DAY} if name.startswith("fail") else None
        extra = {"backwards-ordered": ["--run-backwards", "--reprocess-behavior", "failed"],
                 "fail-ordered": ["--reprocess-behavior", "failed"]}.get(name, [])
        if name == "ordered-default":
            stack.dag_test(dag, "2026-09-03")  # дата в диапазоне уже обработана
        if name.startswith("repeat"):
            run_backfill(dag)  # первый backfill — история уже посчитана
            extra = ["--reprocess-behavior", "completed"] if name == "repeat-completed" else []
        rc, out, bid, stuck = run_backfill(dag, *extra, conf=conf)
    finally:
        stack.pause(dag)
        stack.wait_runs(dag, stall=30)
    c = counts(dag, bid) if bid else {"runs": 0, "success": 0, "failed": 0, "not_run": 0, "first_failed": None}
    try:
        matched, bad = stack.compare(stack.revenue(), stack.truth("v1"))
    except SystemExit:  # витрины нет — ни один день не дошёл до dbt
        matched, bad = 0, {}
    err = [l.strip() for l in out.split("\n") if l.strip()]
    r = {"kind": "backfill", "variant": name, "probe": dag, "cmd": "python3 scripts/run-backfill.py %s" % name,
         "dag": dag, "cli_rc": rc, "cli_error": err[-1][-200:] if (rc and err) else "", "stuck": stuck,
         **c, "matched_days": matched, "days": len(stack.DAYS)}
    if name in REFUSED:
        r["outcome"] = "mismatch" if rc else "match"
        r["detail"] = "backfill отказан: %s" % r["cli_error"] if rc else "backfill принят: запусков %d" % c["runs"]
    else:
        r["outcome"] = "match" if (c["failed"] == 0 and matched == len(stack.DAYS)) else "mismatch"
        r["detail"] = "запусков backfill %d: успешно %d, сбой %d, не выполнено %d; витрина совпала с эталоном %d из %d" % (
            c["runs"], c["success"], c["failed"], c["not_run"], matched, len(stack.DAYS))
    BACKFILL.parse_line(json.dumps(r, ensure_ascii=False))
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
