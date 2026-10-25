"""Ось 2: идемпотентность загрузки дня в dwh.raw.

Режимы (pipeline/load.py): append; delete-insert в одной транзакции;
delete-batch — DELETE фиксируется сразу, INSERT пакетами; merge — upsert по
ключу пакетами. Сценарии на одном дне:
  once         — один прогон (контроль: эталон во всех режимах);
  rerun        — прогон, затем повтор того же дня;
  crash-reload — день уже загружен, повторный прогон падает после k пакетов
                 позиций; счёт после падения и после ещё одного повтора.
Счёт в raw за день: строк позиций, уникальных, дублей и потерь против эталона.

    python3 scripts/run-idempotency.py                   → все варианты, фикстура
    python3 scripts/run-idempotency.py merge crash-reload → один вариант
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import END_MARKER, IDEMPOTENCY  # noqa: E402

FIXTURE = os.path.join(stack.HERE, "fixtures", "idempotency.jsonl")
DAY, CRASH_AFTER = "2026-09-03", 5
MODES = ("append", "delete-insert", "delete-batch", "merge")
SCENARIOS = ("once", "rerun", "crash-reload")


def state(want):
    rows, distinct = (int(x) for x in stack.sql(
        "dwh", "SELECT count(*), count(DISTINCT id) FROM raw.order_items WHERE load_day = '%s'" % DAY)[0])
    return {"rows": rows, "distinct": distinct, "duplicates": rows - distinct, "missing": want - distinct}


def run(mode, crash=None):
    conf = {"mode": mode}
    if crash:
        conf["crash_after"] = crash
    return stack.dag_test("elt_daily", DAY, conf)


def measure(mode, scenario):
    stack.reset_data()
    want = stack.truth("v1")[DAY]["items"]
    runs = [run(mode)]
    after_crash = None
    if scenario == "rerun":
        runs.append(run(mode))
    elif scenario == "crash-reload":
        runs.append(run(mode, CRASH_AFTER))
        after_crash = state(want)
        runs.append(run(mode))
    final = state(want)
    expected_runs = ["success", "failed", "success"] if scenario == "crash-reload" else ["success"] * len(runs)
    r = {"kind": "idempotency", "variant": "%s/%s" % (mode, scenario), "probe": "items %s" % DAY,
         "cmd": "python3 scripts/run-idempotency.py %s %s" % (mode, scenario), "mode": mode, "scenario": scenario,
         "crash_after_batches": CRASH_AFTER if scenario == "crash-reload" else None, "runs": runs,
         "want_items": want, "after_crash": after_crash, **final}
    if runs != expected_runs:
        r["outcome"], r["detail"] = "error", "запуски не те: %s" % runs
    else:
        ok = final["duplicates"] == 0 and final["missing"] == 0
        r["outcome"] = "match" if ok else "mismatch"
        r["detail"] = "итог: дублей %d, потерь %d%s" % (final["duplicates"], final["missing"], (
            "; после падения: строк %d, дублей %d, потерь %d" % (after_crash["rows"], after_crash["duplicates"],
                                                                 after_crash["missing"])) if after_crash else "")
    IDEMPOTENCY.parse_line(json.dumps(r, ensure_ascii=False))
    return r


SHOW = ("variant", "outcome", "detail")


def main():
    stack.up()
    if sys.argv[1:]:
        print(json.dumps({k: measure(*sys.argv[1:3])[k] for k in SHOW}, ensure_ascii=False))
        return
    rows = []
    for m in MODES:
        for s in SCENARIOS:
            rows.append(measure(m, s))
            print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
