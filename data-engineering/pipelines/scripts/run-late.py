"""Ось 5: поздние данные.

Сценарий: дни 01–06.09 обработаны; затем в источник приходят 20 заказов за 05.09
со временем поступления 07.09 02:00 (событие — последний час 05.09, опоздание
2 дня); дальше обрабатываются 07–14.09. Эталон — с поздними заказами. Варианты выгрузки и
витрины:
  control            — поздних заказов нет; выгрузка по времени события;
  event              — выгрузка по времени события (created_at), без пересмотра;
  event-lookback1    — то же, но каждый запуск перезагружает и прошлый день;
  event-lookback2    — перезагружает два прошлых дня;
  ingest             — выгрузка по времени поступления (ingested_at), витрина —
                       таблица, пересчитываемая целиком;
  ingest-inc         — выгрузка по ingested_at, инкрементальная витрина без окна;
  ingest-inc-lookback — инкрементальная витрина с окном пересмотра 1 день от
                       последнего посчитанного;
  ingest-inc-refresh — инкрементальная витрина без окна, в конце dbt --full-refresh.
Счёт — витрина за 05.09 против эталона: выручка, заказы.

    python3 scripts/run-late.py                  → все варианты, фикстура
    python3 scripts/run-late.py ingest-inc       → один вариант
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import END_MARKER, LATE  # noqa: E402

FIXTURE = os.path.join(stack.HERE, "fixtures", "late.jsonl")
LATE_DAY, LATE_N, LAG = "2026-09-05", 20, 2
ARRIVE_AFTER = "2026-09-06"  # приход 07.09 02:00 — после обработки 06.09
# вариант → (conf запуска DAG, витрина, full refresh в конце)
VARIANTS = {
    "control": ({"by": "created_at"}, "daily_revenue", False),
    "event": ({"by": "created_at"}, "daily_revenue", False),
    "event-lookback1": ({"by": "created_at", "lookback": 1}, "daily_revenue", False),
    "event-lookback2": ({"by": "created_at", "lookback": 2}, "daily_revenue", False),
    "ingest": ({"by": "ingested_at"}, "daily_revenue", False),
    "ingest-inc": ({"by": "ingested_at", "models": "+daily_revenue_inc"}, "daily_revenue_inc", False),
    "ingest-inc-lookback": ({"by": "ingested_at", "models": "+daily_revenue_inc", "mart_lookback": 1},
                            "daily_revenue_inc", False),
    "ingest-inc-refresh": ({"by": "ingested_at", "models": "+daily_revenue_inc"}, "daily_revenue_inc", True),
}


def measure(name):
    conf, mart, refresh = VARIANTS[name]
    stack.reset_data()
    late = name != "control"
    runs = []
    for d in stack.DAYS:
        runs.append(stack.dag_test("elt_daily", d, conf))
        if late and d == ARRIVE_AFTER:  # поздние заказы приходят после обработки следующего дня
            stack.ax("python", "-m", "generator.gen", "late", LATE_DAY, str(LATE_N), str(LAG))
    if refresh:
        stack.dbt("run", "--select", mart, "--full-refresh")
    want = stack.truth("v1", (LATE_DAY, LATE_N) if late else None)
    got = stack.revenue("marts." + mart)
    matched, bad = stack.compare(got, want, keys=("revenue", "orders"))
    g = got.get(LATE_DAY, {"revenue": 0, "orders": 0})
    r = {"kind": "late", "variant": name, "probe": "late %s x%d lag%d" % (LATE_DAY, LATE_N, LAG) if late else "no-late",
         "lag_days": LAG if late else 0,
         "cmd": "python3 scripts/run-late.py %s" % name, "conf": conf, "mart": mart, "full_refresh": refresh,
         "late_orders": LATE_N if late else 0, "runs_ok": runs.count("success"),
         "orders_missing": want[LATE_DAY]["orders"] - g["orders"],
         "revenue_missing": want[LATE_DAY]["revenue"] - g["revenue"], "matched_days": matched, "mismatch": bad}
    if r["runs_ok"] != len(stack.DAYS):
        r["outcome"], r["detail"] = "error", "упало запусков: %d" % (len(stack.DAYS) - r["runs_ok"])
    else:
        r["outcome"] = "match" if not bad else "mismatch"
        r["detail"] = "за %s не хватает заказов %d (выручки %d); совпало дней %d из %d" % (
            LATE_DAY, r["orders_missing"], r["revenue_missing"], matched, len(stack.DAYS))
    LATE.parse_line(json.dumps(r, ensure_ascii=False))
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
