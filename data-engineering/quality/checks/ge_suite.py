"""Набор ожиданий Great Expectations на партию raw.orders.

Построчные правила — те же, что у тестов dbt (кроме ссылки на справочник
клиентов: в ядре GE нет ожидания на внешний ключ между таблицами), плюс две
проверки партии целиком: среднее суммы в пределах ±20% от исторического и
число строк в обычном коридоре. Печатает итог «RESULT {json}».

    python -m checks.ge_suite
"""

import json
import os

import great_expectations as gx
import pandas as pd
import psycopg2

HIST_MEAN = 10250  # среднее суммы по чистым партиям (генератор: 500…20000 копеек)


def main():
    with psycopg2.connect(os.environ["STAND_DWH_DSN"]) as c:
        df = pd.read_sql("SELECT * FROM raw.orders", c)
    ctx = gx.get_context(mode="ephemeral")
    batch = ctx.data_sources.add_pandas("raw").add_dataframe_asset("orders").add_batch_definition_whole_dataframe(
        "batch").get_batch(batch_parameters={"dataframe": df})
    E = gx.expectations
    suite = [
        ("not_null:customer_id", E.ExpectColumnValuesToNotBeNull(column="customer_id")),
        ("unique:id", E.ExpectColumnValuesToBeUnique(column="id")),
        ("in_set:status", E.ExpectColumnValuesToBeInSet(column="status", value_set=["paid", "cancelled", "refunded"])),
        ("between:amount", E.ExpectColumnValuesToBeBetween(column="amount", min_value=1, max_value=100000000)),
        ("mean:amount", E.ExpectColumnMeanToBeBetween(column="amount", min_value=HIST_MEAN * 0.8,
                                                      max_value=HIST_MEAN * 1.2)),
        ("rows:batch", E.ExpectTableRowCountToBeBetween(min_value=200, max_value=400)),
    ]
    out = {}
    for name, exp in suite:
        r = batch.validate(exp)
        out[name] = {"success": bool(r.success),
                     "unexpected": int(r.result.get("unexpected_count") or 0)}
    print("RESULT " + json.dumps(out, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
