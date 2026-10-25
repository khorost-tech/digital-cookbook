"""Ось 1: ETL против ELT при смене бизнес-правила.

Три схемы:
  etl       — ETL с сохранением только агрегата (DAG etl_daily): выручка
              считается в Python до загрузки, в хранилище — итог дня;
  etl-facts — ETL с подробными фактами (DAG etl_facts_daily): преобразование
              до загрузки, но в хранилище — факт заказа со статусом, выручку по
              правилу считает SQL в хранилище;
  elt       — ELT с сохранением raw (DAG elt_daily): сырые строки в dwh.raw,
              выручку считает dbt.
Все прогоняются за 14 дней на правиле v1 (контроль — совпадение с эталоном).
Затем правило меняется на v2 (без возвратов) и история пересчитывается так, как
позволяет схема: etl — заново выгружая каждый день, etl-facts — SQL над
фактами, elt — одним dbt run из raw.
Запросы к источнику считает pg_stat_statements базы shop, а не сам пайплайн.

    python3 scripts/run-elt.py              → все варианты, фикстура
    python3 scripts/run-elt.py elt          → одна схема, строки JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import ELT, END_MARKER  # noqa: E402

FIXTURE = os.path.join(stack.HERE, "fixtures", "elt.jsonl")
TABLE = {"etl": "marts_etl.daily_revenue", "etl-facts": "marts_etl_facts.daily_revenue", "elt": "marts.daily_revenue"}
DAG = {"etl": "etl_daily", "etl-facts": "etl_facts_daily", "elt": "elt_daily"}
EXCLUDED = {"v1": "('cancelled')", "v2": "('cancelled', 'refunded')"}


def build_facts(rule):
    """Выручка дня по правилу — SQL над фактом заказа в хранилище, без источника."""
    stack.sql("dwh", "DROP TABLE IF EXISTS marts_etl_facts.daily_revenue; "
                     "CREATE TABLE marts_etl_facts.daily_revenue AS SELECT order_day, "
                     "sum(amount) FILTER (WHERE status NOT IN %s) AS revenue, count(*) AS orders, "
                     "sum(items) AS items FROM marts_etl_facts.fct_order GROUP BY 1" % EXCLUDED[rule])


def source_queries():
    rows = stack.sql("shop", "SELECT coalesce(sum(calls), 0) FROM pg_stat_statements "
                             "WHERE query ILIKE '%app.order%' AND dbid = (SELECT oid FROM pg_database "
                             "WHERE datname = 'shop')")
    return int(rows[0][0])


def reset_counter():
    stack.sql("shop", "SELECT pg_stat_statements_reset()")


def row(scheme, rule, step, queries, failed):
    got = stack.revenue(TABLE[scheme])
    matched, bad = stack.compare(got, stack.truth(rule))
    r = {"kind": "elt", "variant": "%s/%s" % (scheme, step), "probe": "14-days",
         "cmd": "python3 scripts/run-elt.py %s" % scheme, "scheme": scheme, "rule": rule, "step": step,
         "matched_days": matched, "days": len(stack.DAYS), "source_queries": queries, "failed_runs": failed,
         "mismatch": bad}
    if failed:
        r["outcome"], r["detail"] = "error", "упало запусков DAG: %d" % failed
    else:
        r["outcome"] = "match" if not bad else "mismatch"
        r["detail"] = "совпало с эталоном %s дней %d из %d, запросов к источнику %d" % (
            rule, matched, len(stack.DAYS), queries)
    ELT.parse_line(json.dumps(r, ensure_ascii=False))
    return r


def measure(scheme):
    stack.reset_data()
    dag = DAG[scheme]
    reset_counter()
    failed = sum(stack.dag_test(dag, d, {"rule": "v1"}) != "success" for d in stack.DAYS)
    if scheme == "etl-facts" and not failed:
        build_facts("v1")
    rows = [row(scheme, "v1", "initial", source_queries(), failed)]
    reset_counter()  # смена правила: пересчитать историю тем, что позволяет схема
    if scheme == "etl":
        failed = sum(stack.dag_test(dag, d, {"rule": "v2"}) != "success" for d in stack.DAYS)
    elif scheme == "etl-facts":
        build_facts("v2")
        failed = 0
    else:
        failed = 0 if stack.dbt("run", "--vars", '{"rule": "v2"}', check=False)[0] == 0 else 1
    rows.append(row(scheme, "v2", "rule-change", source_queries(), failed))
    return rows


SHOW = ("variant", "outcome", "detail")


def main():
    stack.up()
    schemes = sys.argv[1:] or ["etl", "etl-facts", "elt"]
    rows = []
    for s in schemes:
        for r in measure(s):
            rows.append(r)
            print(json.dumps({k: r[k] for k in SHOW}, ensure_ascii=False), flush=True)
    if not sys.argv[1:]:
        with open(FIXTURE, "w", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
            f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
