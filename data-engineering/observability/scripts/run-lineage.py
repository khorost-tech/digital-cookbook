"""Ось 1: lineage dbt → OpenLineage → Marquez и затронутое ниже по потоку.

Данные — все 28 дней; dbt-ol run передаёт события в чистую базу Marquez.
  tables     — наборы ниже raw.orders по графу Marquez (обход исходящих рёбер)
               против графа манифеста dbt (контроль);
  columns    — наборы и поля ниже raw.orders.amount по фасетам columnLineage;
  columns-status — то же от raw.orders.status, против моделей, чей SQL
               читает status (в том числе в WHERE): косвенные зависимости;
  column-api — ответ API column-lineage Marquez на тот же вопрос.

    python3 scripts/run-lineage.py               → все варианты, фикстура
"""

import json
import os
import re
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, LINEAGE  # noqa: E402

NS = "postgres://postgres:5432"
SRC = "dwh.raw.orders"
STATE = {}


def prepare():
    stack.gen("reset")
    for d in json.loads(stack.gen("incidents"))["days"]:
        stack.gen("load", d)
    stack.reset_marquez()
    stack.dbt_ol("run")
    settle()
    STATE["ready"] = True


def facets():
    ds = stack.marquez("/api/v1/namespaces/%s/datasets" % NS.replace("/", "%2F").replace(":", "%3A"),
                       limit=100)["datasets"]
    return {d["name"]: (d.get("facets", {}).get("columnLineage", {}).get("fields") or {}) for d in ds}


def settle(timeout=300):
    """Marquez пишет события асинхронно: сразу после dbt-ol run часть фасетов
    ещё не сохранена, и граф колонок выходит неполным. Ждём, пока у каждой
    модели манифеста есть фасет columnLineage и два чтения подряд совпали."""
    want = {"dwh." + n for n in manifest_downstream()} | {"dwh.staging.stg_customers"}
    prev, start = None, time.time()
    while time.time() - start < timeout:
        cur = facets()
        if all(cur.get(n) for n in want) and cur == prev:
            return
        prev = cur
        time.sleep(5)
    raise SystemExit("Marquez не сохранил фасеты за %d с" % timeout)


def manifest():
    return json.loads(stack.tools("cat", "/opt/stand/.dbt-target/manifest.json")[1])


def manifest_downstream(uses=None):
    """Модели ниже raw.orders по графу dbt; uses — только те, чей скомпилированный
    SQL упоминает колонку (выводит её или фильтрует, соединяет по ней)."""
    m = manifest()
    start = [k for k in m["sources"] if k.endswith(".raw.orders")][0]
    seen, todo = set(), [start]
    while todo:
        for ch in m["child_map"].get(todo.pop(), []):
            if ch.startswith("model.") and ch not in seen:
                seen.add(ch)
                todo.append(ch)
    if uses:
        seen = {n for n in seen if re.search(r"\b%s\b" % uses, m["nodes"][n].get("compiled_code") or "")}
    return sorted(m["nodes"][n]["schema"] + "." + m["nodes"][n]["name"] for n in seen)


def marquez_downstream():
    g = stack.marquez("/api/v1/lineage", nodeId="dataset:%s:%s" % (NS, SRC), depth=20)["graph"]
    out = {n["id"]: [e["destination"] for e in n.get("outEdges", [])] for n in g}
    seen, todo = set(), ["dataset:%s:%s" % (NS, SRC)]
    while todo:
        for nxt in out.get(todo.pop(), []):
            if nxt not in seen:
                seen.add(nxt)
                todo.append(nxt)
    return sorted(n.split(":", 3)[3].split(".", 1)[1] for n in seen if n.startswith("dataset:"))


def column_downstream(field="amount"):
    edges = []  # (вход набор, поле) → (выход набор, поле)
    for name, fields in facets().items():
        for out_field, v in fields.items():
            for i in v.get("inputFields", []):
                edges.append(((i["name"], i["field"]), (name, out_field)))
    seen, todo = set(), [(SRC, field)]
    while todo:
        cur = todo.pop()
        for a, b in edges:
            if a == cur and b not in seen:
                seen.add(b)
                todo.append(b)
    return sorted("%s.%s" % (n.split(".", 1)[1], f) for n, f in seen)


def measure(name):
    if not STATE.get("ready"):
        prepare()
    r = {"probe": "downstream of raw.orders"}
    if name == "tables":
        got, want = marquez_downstream(), manifest_downstream()
        r.update({"datasets": got, "manifest": want})
        r["outcome"] = "match" if got == want else "mismatch"
        r["detail"] = "ниже raw.orders по Marquez %d наборов, по манифесту dbt %d" % (len(got), len(want))
    elif name == "columns":
        fields = column_downstream()
        datasets = sorted({f.rsplit(".", 1)[0] for f in fields})
        r.update({"fields": fields, "datasets": datasets})
        r["outcome"] = "match" if "marts.status_counts" not in datasets else "mismatch"
        r["detail"] = "от raw.orders.amount: полей %d в %d наборах; status_counts %s" % (
            len(fields), len(datasets), "задета" if "marts.status_counts" in datasets else "не задета")
        r["uses"] = manifest_downstream("amount")
    elif name == "columns-status":
        fields = column_downstream("status")
        datasets = sorted({f.rsplit(".", 1)[0] for f in fields})
        uses = manifest_downstream("status")
        missed = [d for d in uses if d not in datasets]
        r.update({"fields": fields, "datasets": datasets, "uses": uses, "missed": missed})
        r["outcome"] = "match" if not missed else "mismatch"
        r["detail"] = "от raw.orders.status по фасетам %d наборов; SQL читает status в %d; не видны: %s" % (
            len(datasets), len(uses), ", ".join(missed) or "—")
    else:
        code = ("import os, requests; r = requests.get(os.environ['MARQUEZ_URL'] + '/api/v1/column-lineage', "
                "params={'nodeId': 'datasetField:%s:%s:amount', 'depth': 20, 'withDownstream': 'true'}, "
                "timeout=60); print('RESULT ' + __import__('json').dumps({'status': r.status_code, "
                "'body': r.text[:120]}))" % (NS, SRC))
        res = stack.result(stack.tools("python", "-c", code)[1])
        r.update({"http_status": res["status"]})
        r["outcome"] = "match" if res["status"] == 200 else "mismatch"
        r["detail"] = "API column-lineage Marquez ответил %d" % res["status"]
    return r


if __name__ == "__main__":
    common.run("lineage", measure, ("tables", "columns", "columns-status", "column-api"), LINEAGE, END_MARKER)
