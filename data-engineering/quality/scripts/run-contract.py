"""Ось 3: контракт модели dbt против смены схемы и смысла источника.

Модель orders_contracted — с contract: enforced (имена и типы колонок),
orders_uncontracted — select * без контракта; над каждой — витрина выручки.
Источник:
  control — исходная схема (контроль: всё строится);
  numeric — сумма стала numeric(12,2), значения те же;
  dropped — колонку суммы удалили;
  units   — суммы в рублях вместо копеек, тип тот же.
Счёт: что построилось, выручка против контроля, поймало ли смену единиц
ожидание среднего GE.

    python3 scripts/run-contract.py              → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import CONTRACT, END_MARKER  # noqa: E402

LOAD = {"control": ("clean",), "numeric": ("clean", "numeric"), "dropped": ("clean", "drop-amount"),
        "units": ("units",)}
CONTROL_REVENUE = {}


def status(node):
    rs = [r for r in stack.dbt_results()["results"] if r["unique_id"].split(".")[-1] == node]
    return rs[0]["status"] if rs else "not-run"


def revenue(model):
    try:
        v = stack.sql("SELECT revenue FROM contract.%s" % model)[0][0]
        return int(float(v)) if v else 0
    except SystemExit:
        return None


def measure(name):
    stack.sql("DROP SCHEMA IF EXISTS contract CASCADE")
    stack.gen("load", *LOAD[name])
    stack.dbt("run", "--select", "contract", check=False)
    st = {m: status(m) for m in ("orders_contracted", "revenue_contracted", "orders_uncontracted",
                                 "revenue_uncontracted")}
    rev = {"contracted": revenue("revenue_contracted"), "uncontracted": revenue("revenue_uncontracted")}
    if name == "control":
        CONTROL_REVENUE["v"] = rev["contracted"]
    ge = stack.ge() if "drop-amount" not in LOAD[name] else {}
    base = CONTROL_REVENUE.get("v")
    r = {"probe": "source %s" % name, "status": st, "revenue": rev, "control_revenue": base,
         "ge_mean_failed": (not ge["mean:amount"]["success"]) if ge else None,
         "contracted_built": st["orders_contracted"] == "success",
         "uncontracted_built": st["orders_uncontracted"] == "success",
         "downstream_built": st["revenue_uncontracted"] == "success"}
    ratio = (rev["uncontracted"] / base) if (base and rev["uncontracted"] is not None) else None
    r["uncontracted_revenue_ratio"] = round(ratio, 4) if ratio is not None else None
    wrong_silently = r["uncontracted_built"] and r["downstream_built"] and ratio is not None and abs(ratio - 1) > 1e-9
    r["outcome"] = "mismatch" if wrong_silently else "match"
    r["detail"] = "с контрактом: %s; без контракта: модель %s, витрина %s, выручка %s от контроля; среднее GE %s" % (
        st["orders_contracted"], st["orders_uncontracted"], st["revenue_uncontracted"],
        "—" if ratio is None else "×%.4f" % ratio,
        "—" if r["ge_mean_failed"] is None else ("упало" if r["ge_mean_failed"] else "в норме"))
    return r


if __name__ == "__main__":
    common.run("contract", measure, tuple(LOAD), CONTRACT, END_MARKER)
