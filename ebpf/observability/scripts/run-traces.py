"""Ось 1: трейсы Beyla (eBPF, svc-plain) против OTEL SDK (svc-sdk) против эталона.

Генератор отправляет на обе сборки одинаковый план и печатает счёт. Варианты:
  sdk-routes / beyla-routes — серверных спанов на маршрут и статус против
      отправленного (контроль — SDK);
  beyla-route-names        — http.route у Beyla: шаблон ServeMux и эвристика
      на порту без шаблона; сколько разных путей ушло в одно имя;
  sdk-sql / beyla-sql      — SQL-спаны против числа запросов к базе;
  sdk-links / beyla-links  — SQL-спаны в одном трейсе с HTTP-спаном запроса
      и с найденным родителем;
  beyla-internal           — служебные спаны Beyla на HTTP-запрос.

    python3 scripts/run-traces.py              → все варианты, фикстура
"""

import collections
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, TRACES  # noqa: E402

SERVICE = {"sdk": "svc-sdk", "beyla": "svc-beyla"}
TARGET = {"sdk": "svc-sdk", "beyla": "svc-plain"}
SERVER, CLIENT = 2, 3
# Запросы к базе на маршрут: /users/{id} — один SELECT, /orders — один INSERT.
SQL_TRUTH = {"SELECT": "GET /users/{id}", "INSERT": "POST /orders"}
VARIANTS = ("sdk-routes", "beyla-routes", "beyla-route-names", "sdk-sql", "beyla-sql", "sdk-links", "beyla-links",
            "beyla-internal")
STATE = {}


def prepare():
    stack.reset_telemetry()
    # сервисы стартуют заново при работающем коллекторе: CREATE TABLE при старте
    # попадает в выгрузку SDK детерминированно; Beyla цепляется к новому процессу позже
    stack.compose("restart", "svc-sdk", "svc-plain")
    STATE["warmup"] = {who: stack.warmup(TARGET[who], SERVICE[who]) for who in ("beyla", "sdk")}
    STATE["truth"] = {who: stack.gen(TARGET[who]) for who in ("beyla", "sdk")}
    # спаны прогрева — и всё в их трейсах — в счёт не входят
    allspans = stack.settle(list(SERVICE.values()))
    warm = {sp["traceId"] for _, sp, a in allspans if sp["name"].endswith("/warmup") or a.get("http.route") == "/warmup"}
    STATE["spans"] = [(svc, sp, a) for svc, sp, a in allspans if sp["traceId"] not in warm]


def mine(who):
    return [(sp, a) for svc, sp, a in STATE["spans"] if svc == SERVICE[who]]


def route_key(sp, a):
    if "rpc.method" in a:
        return "/" + a["rpc.method"].lstrip("/")
    name = sp["name"]
    return "GET /v2/users/{id}" if name.startswith("GET /v2/users") else name


def status(a):
    return str(a.get("http.response.status_code") or a.get("rpc.response.status_code") or
               {0: "OK"}.get(a.get("rpc.grpc.status_code"), a.get("rpc.grpc.status_code")))


def measure_routes(who):
    sent = STATE["truth"][who]["sent"]
    got = collections.defaultdict(collections.Counter)
    for sp, a in mine(who):
        if sp["kind"] == SERVER:
            got[route_key(sp, a)][status(a)] += 1
    got = {k: dict(v) for k, v in got.items()}
    want = {k: v for k, v in sent.items()}
    bad = sorted(k for k in set(want) | set(got) if want.get(k) != got.get(k))
    return {"probe": "server spans per route", "sent": want, "got": got, "mismatched_routes": bad,
            "outcome": "match" if not bad else "mismatch",
            "detail": "маршрутов %d, совпали со счётом генератора %d%s" % (
                len(want), len(want) - len(bad), "; расхождения: %s" % ", ".join(bad) if bad else "")}


def measure_names():
    names = collections.defaultdict(collections.Counter)
    paths = collections.defaultdict(set)
    for sp, a in mine("beyla"):
        if sp["kind"] == SERVER and "http.route" in a:
            port = a.get("server.port")
            names[port][a["http.route"]] += 1
            paths[(port, a["http.route"])].add(a.get("url.path"))
    v2 = names.get("8081", {})
    route = next(iter(v2), None)
    return {"probe": "http.route on :8080 (ServeMux) and :8081 (no pattern)",
            "routes_8080": sorted(names.get("8080", {})), "routes_8081": sorted(v2),
            "distinct_paths_8081": len(paths.get(("8081", route), ())),
            "outcome": "match" if len(v2) == 1 else "mismatch",
            "detail": ":8080 — %s; :8081 — %d разных путей → %s" % (
                ", ".join(sorted(names.get("8080", {}))), len(paths.get(("8081", route), ())),
                ", ".join(sorted(v2)) or "—")}


def sql_op(sp, a):
    if "db.operation.name" in a:
        return a["db.operation.name"]
    text = (a.get("db.query.text") or a.get("db.statement") or "").split()
    return text[0].upper() if text else sp["name"]


def measure_sql(who):
    sent = STATE["truth"][who]["sent"]
    want = {op: sum(sent[route].values()) for op, route in SQL_TRUTH.items()}
    got = collections.Counter()
    for sp, a in mine(who):
        if sp["kind"] == CLIENT and sp["name"] in ("sql.conn.exec", "sql.conn.query") or \
                (who == "beyla" and sp["kind"] == CLIENT):
            got[sql_op(sp, a)] += 1
    got = dict(got)
    extra = {k: v for k, v in got.items() if k not in want}
    ok = all(got.get(k) == v for k, v in want.items())
    # CREATE TABLE при старте: сколько раз попал в выгрузку (1 или 2 — первый старт до
    # сброса выгрузки и перезапуск) — вне сравнения; публикуется, виден ли он вообще
    return {"probe": "SQL client spans", "want": want, "got_select_insert": {k: got.get(k, 0) for k in want},
            "extra~": extra, "startup_create_seen": "CREATE" in extra,
            "other_extra": sorted(k for k in extra if k != "CREATE"),
            "outcome": "match" if ok and not extra else "mismatch",
            "detail": "SELECT %s из %d, INSERT %s из %d%s" % (
                got.get("SELECT", 0), want["SELECT"], got.get("INSERT", 0), want["INSERT"],
                "; сверх запросов: %s" % ", ".join("%s %d" % kv for kv in sorted(extra.items())) if extra else "")}


def measure_links(who):
    sp_all = mine(who)
    by_id = {(sp["traceId"], sp["spanId"]): sp for sp, _ in sp_all}
    server_traces = {sp["traceId"] for sp, _ in sp_all if sp["kind"] == SERVER}
    sql = [sp for sp, a in sp_all if sp["kind"] == CLIENT and sql_op(sp, a) in SQL_TRUTH]
    same_trace = sum(sp["traceId"] in server_traces for sp in sql)
    parent = sum(by_id.get((sp["traceId"], sp.get("parentSpanId", ""))) is not None for sp in sql)
    return {"probe": "SQL spans → HTTP span", "sql_spans": len(sql), "same_trace": same_trace,
            "parent_found": parent, "outcome": "match" if parent == len(sql) else "mismatch",
            "detail": "SQL-спанов %d: в трейсе HTTP-запроса %d, родитель найден %d" % (len(sql), same_trace, parent)}


def measure_internal():
    sent = STATE["truth"]["beyla"]["sent"]
    http = sum(sum(v.values()) for k, v in sent.items() if not k.startswith("/grpc"))
    internal = collections.Counter(sp["name"] for sp, _ in mine("beyla") if sp["kind"] == 1)
    # запросов прогрева до первого спана Beyla: от прогона к прогону разное («~»)
    return {"probe": "internal spans", "http_requests": http, "internal": dict(internal),
            "per_request": round(sum(internal.values()) / http, 2), "warmup_requests~": STATE["warmup"]["beyla"],
            "outcome": "match" if all(v == http for v in internal.values()) else "mismatch",
            "detail": "HTTP-запросов %d, служебных спанов: %s; запросов прогрева до первого спана — %d" % (
                http, ", ".join("%s %d" % kv for kv in sorted(internal.items())), STATE["warmup"]["beyla"])}


def measure(name):
    if not STATE:
        prepare()
    who = name.split("-")[0]
    if name.endswith("-routes"):
        return measure_routes(who)
    if name == "beyla-route-names":
        return measure_names()
    if name.endswith("-sql"):
        return measure_sql(who)
    if name.endswith("-links"):
        return measure_links(who)
    return measure_internal()


if __name__ == "__main__":
    common.run("traces", measure, VARIANTS, TRACES, END_MARKER)
