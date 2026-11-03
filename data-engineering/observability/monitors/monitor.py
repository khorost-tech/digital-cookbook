"""Мониторы данных: метрики дня после загрузки и детекторы тревог.

После загрузки дня D монитор снимает метрики «на момент проверки» — D+1 06:00:
строк за день, среднее суммы (в день без строк — пусто, распределение его пропускает), выход за построчный диапазон, возраст последней
загрузки, состав колонок raw.orders. Детекторы смотрят на метрики дня и
историю до него: постоянные пороги и пороги по истории.

    python -m monitors.monitor snapshot DAY   метрики дня в monitor.metrics
    python -m monitors.monitor evaluate       тревоги детекторов за дни оценки (JSON)
    python -m monitors.monitor metrics        метрики дней оценки (JSON)
"""

import datetime as dt
import json
import os
import statistics
import sys

import psycopg2

EVAL_FROM = dt.date(2026, 9, 1)


def connect():
    return psycopg2.connect(os.environ["STAND_DWH_DSN"])


def snapshot(day):
    check = dt.datetime.combine(day + dt.timedelta(days=1), dt.time(6, 0))
    with connect() as c, c.cursor() as cur:
        cur.execute("CREATE SCHEMA IF NOT EXISTS monitor; CREATE TABLE IF NOT EXISTS monitor.metrics "
                    "(day date PRIMARY KEY, rows int, mean_amount float, out_of_range int, age_hours float, "
                    "columns text)")
        cur.execute("SELECT count(*), avg(amount), count(*) FILTER (WHERE amount < 1 OR amount > 100000000) "
                    "FROM raw.orders WHERE created_at::date = %s AND loaded_at <= %s", (day, check))
        rows, mean, oor = cur.fetchone()
        cur.execute("SELECT extract(epoch FROM %s - max(loaded_at)) / 3600 FROM raw.orders WHERE loaded_at <= %s",
                    (check, check))
        age = cur.fetchone()[0]
        cur.execute("SELECT string_agg(column_name || ':' || data_type, ',' ORDER BY column_name) "
                    "FROM information_schema.columns WHERE table_schema = 'raw' AND table_name = 'orders'")
        cols = cur.fetchone()[0]
        cur.execute("INSERT INTO monitor.metrics VALUES (%s,%s,%s,%s,%s,%s) ON CONFLICT (day) DO UPDATE SET "
                    "rows = EXCLUDED.rows, mean_amount = EXCLUDED.mean_amount, out_of_range = EXCLUDED.out_of_range, "
                    "age_hours = EXCLUDED.age_hours, columns = EXCLUDED.columns",
                    (day, rows, None if mean is None else float(mean), oor, float(age), cols))


def metrics():
    with connect() as c, c.cursor() as cur:
        cur.execute("SELECT day, rows, mean_amount, out_of_range, age_hours, columns FROM monitor.metrics ORDER BY day")
        return [dict(zip(("day", "rows", "mean", "oor", "age", "columns"), r)) for r in cur.fetchall()]


def detectors(m, hist, flagged=frozenset()):
    """{детектор: тревога ли} для метрик дня m по истории hist (дни до m).
    flagged — дни, где distribution-history-clean уже тревожил: в его историю
    они не идут, чтобы затянувшийся сбой не стал новой нормой."""
    same_dow = [h["rows"] for h in hist if h["day"].weekday() == m["day"].weekday()][-3:]
    means = [h["mean"] for h in hist if h["mean"] is not None][-14:]
    ages = [h["age"] for h in hist][-14:]
    mu, sd = statistics.mean(means), statistics.pstdev(means)
    clean = [h["mean"] for h in hist if h["mean"] is not None and h["day"] not in flagged][-14:]
    cmu, csd = statistics.mean(clean), statistics.pstdev(clean)
    amu, asd = statistics.mean(ages), statistics.pstdev(ages)
    return {
        "volume-static": m["rows"] < 200,
        "volume-history": bool(same_dow) and m["rows"] < 0.7 * statistics.mean(same_dow),
        "freshness-static": m["age"] > 24,
        "freshness-history": m["age"] > amu + max(3 * asd, 2),
        "schema-diff": bool(hist) and m["columns"] != hist[-1]["columns"],
        "distribution-static": m["oor"] > 0,
        "distribution-history": m["mean"] is not None and abs(m["mean"] - mu) > 3 * sd,
        "distribution-history-clean": m["mean"] is not None and abs(m["mean"] - cmu) > 3 * csd,
    }


def evaluate():
    ms = metrics()
    out, flagged = {}, set()
    for i, m in enumerate(ms):
        if m["day"] < EVAL_FROM:
            continue
        out[m["day"].isoformat()] = detectors(m, ms[:i], frozenset(flagged))
        if out[m["day"].isoformat()]["distribution-history-clean"]:
            flagged.add(m["day"])
    return out


def main():
    if sys.argv[1] == "snapshot":
        snapshot(dt.date.fromisoformat(sys.argv[2]))
    elif sys.argv[1] == "metrics":
        print("RESULT " + json.dumps({m["day"].isoformat(): {"rows": m["rows"], "age": m["age"]}
                                      for m in metrics() if m["day"] >= EVAL_FROM}), flush=True)
    else:
        print("RESULT " + json.dumps(evaluate()), flush=True)


if __name__ == "__main__":
    main()
