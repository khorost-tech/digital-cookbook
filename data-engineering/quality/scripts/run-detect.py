"""Ось 1: какой дефект партии ловит какой набор проверок.

Для каждого дефекта генератор кладёт партию в raw.orders; затем — тесты dbt на
staging (unique, not_null, relationships, accepted_values, сингулярный тест
диапазона), dbt source freshness и набор Great Expectations (checks/ge_suite.py).
Счёт: какие проверки упали и сколько строк указали; плохие строки — из генератора.
Контроль — чистая партия.

    python3 scripts/run-detect.py                → все варианты, фикстура
    python3 scripts/run-detect.py shift          → один вариант
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import DETECT, END_MARKER  # noqa: E402

DEFECTS = ("clean", "nulls", "duplicates", "range", "enum", "orphans", "stale", "shift", "volume")
DBT_TESTS = {"unique_stg_orders_id": "unique", "not_null_stg_orders_customer_id": "not_null",
             "relationships_stg_orders": "relationships", "accepted_values_stg_orders_status": "accepted_values",
             "amount_range": "amount_range"}


def dbt_checks():
    stack.dbt("build", "--select", "stg_customers", "stg_orders", "amount_range", "test_type:generic", check=False)
    out = {}
    for r in stack.dbt_results()["results"]:
        name = r["unique_id"].split(".")[2]
        key = next((v for k, v in DBT_TESTS.items() if name.startswith(k)), None)
        if key:
            out[key] = {"failed": r["status"] in ("fail", "error", "warn"), "failures": int(r.get("failures") or 0)}
    stack.dbt("source", "freshness", check=False)
    src = [s for s in stack.dbt_results("sources")["results"] if s["unique_id"].endswith("raw.orders")][0]
    out["freshness"] = {"failed": src["status"] in ("error", "warn"), "failures": 0}
    return out


def measure(name):
    stack.sql("DROP SCHEMA IF EXISTS staging CASCADE; DROP SCHEMA IF EXISTS marts CASCADE")
    stack.gen("load", name)
    truth = stack.gen("bad", name)
    dbt, ge = dbt_checks(), stack.ge()
    ge = {k: {"failed": not v["success"], "failures": v["unexpected"]} for k, v in ge.items()}
    dbt_caught = sorted(k for k, v in dbt.items() if v["failed"])
    ge_caught = sorted(k for k, v in ge.items() if v["failed"])
    r = {"probe": "batch 2026-09-10", "bad": truth["bad"], "rows": truth["rows"], "dbt": dbt, "ge": ge,
         "dbt_caught": dbt_caught, "ge_caught": ge_caught, "dbt_detected": bool(dbt_caught),
         "ge_detected": bool(ge_caught)}
    if name == "clean":
        r["outcome"] = "match" if not dbt_caught and not ge_caught else "mismatch"
    else:
        r["outcome"] = "match" if (dbt_caught or ge_caught) else "mismatch"
    r["detail"] = "плохих строк %d из %d; dbt: %s; GE: %s" % (truth["bad"], truth["rows"],
                                                           ", ".join(dbt_caught) or "—", ", ".join(ge_caught) or "—")
    return r


if __name__ == "__main__":
    common.run("detect", measure, DEFECTS, DETECT, END_MARKER)
