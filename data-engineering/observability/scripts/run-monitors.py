"""Ось 2: детекторы мониторов против эталона инцидентов генератора.

28 дней: загрузка дня, затем снимок метрик на момент проверки (D+1 06:00).
Оценка — 01–14.09, история — дни до оцениваемого. Вариант — детектор; его
тревоги сверяются с эталоном своего семейства (expected.json, monitors.truth).

    python3 scripts/run-monitors.py                  → все детекторы, фикстура
    python3 scripts/run-monitors.py volume-static    → один детектор
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, MONITORS  # noqa: E402

DETECTORS = ("volume-static", "volume-static-best", "volume-history", "freshness-static", "freshness-history", "schema-diff",
             "distribution-static", "distribution-history", "distribution-history-clean",
             "drift/distribution-history", "drift/distribution-history-clean")
# Семейство детектора → какие инциденты генератора оно обязано заметить к проверке.
FAMILY = {"volume": ("volume", "freshness"), "freshness": ("freshness",), "schema": ("schema",),
          "distribution": ("distribution",)}
STATES = {}


def load_scenario(scenario):
    stack.sql("DROP SCHEMA IF EXISTS monitor CASCADE")
    stack.gen("reset")
    inc = json.loads(stack.gen("incidents", scenario))
    for d in inc["days"]:
        stack.gen("load", d, scenario)
        stack.monitor("snapshot", d)
    return inc["incidents"], stack.result(stack.monitor("evaluate"))


def prepare_drift():
    """Длительная деградация: с 02.09 каждый день треть сумм в рублях."""
    incidents, alerts = load_scenario("drift")
    STATES["drift"] = {"incidents": incidents, "alerts": alerts}


def prepare():
    STATE = STATES.setdefault("base", {})
    STATE["incidents"], STATE["alerts"] = load_scenario("base")
    # Лучший постоянный порог объёма, подобранный задним числом по дням оценки:
    # наименьший «строк меньше T», который ловит все инциденты объёма.
    rows = {d: m["rows"] for d, m in stack.result(stack.monitor("metrics")).items()}
    truth = [d for d, k in STATE["incidents"].items() if k in FAMILY["volume"] and d in rows]
    t = max(rows[d] for d in truth) + 1
    for d in STATE["alerts"]:
        STATE["alerts"][d]["volume-static-best"] = rows[d] < t
    STATE["best_threshold"] = t
    STATE["rows"] = rows


def measure(name):
    scenario, _, detector = name.rpartition("/")
    scenario = scenario or "base"
    if scenario not in STATES:
        (prepare_drift if scenario == "drift" else prepare)()
    STATE = STATES[scenario]
    family = detector.split("-")[0]
    days = sorted(STATE["alerts"])
    truth = sorted(d for d, k in STATE["incidents"].items() if k in FAMILY[family] and d in STATE["alerts"])
    alerts = sorted(d for d in days if STATE["alerts"][d][detector])
    tp = [d for d in alerts if d in truth]
    fp = [d for d in alerts if d not in truth]
    fn = [d for d in truth if d not in alerts]
    extra = {}
    if name == "volume-static-best":
        extra = {"threshold": STATE["best_threshold"], "rows_truth": [STATE["rows"][d] for d in truth],
                 "rows_fp": [STATE["rows"][d] for d in fp], "rows_by_day": STATE["rows"]}
    return {"probe": "%s, %d days %s..%s" % (scenario, len(days), days[0], days[-1]), "truth": truth, "alerts": alerts, **extra,
            "tp": len(tp), "fp": len(fp), "fn": len(fn), "fp_days": fp, "fn_days": fn,
            "outcome": "match" if not fp and not fn else "mismatch",
            "detail": "%sпоймано %d из %d, ложных %d%s%s" % (
                "порог меньше %d строк: " % STATE["best_threshold"] if name == "volume-static-best" else "",
                len(tp), len(truth), len(fp), " (%s)" % ", ".join(fp) if fp else "",
                "; пропущено: %s" % ", ".join(fn) if fn else "")}


if __name__ == "__main__":
    common.run("monitors", measure, DETECTORS, MONITORS, END_MARKER)
