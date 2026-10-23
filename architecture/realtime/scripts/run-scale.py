"""Ось 3: несколько инстансов → fixtures/scale.jsonl.

Два инстанса одного сервера за Angie (по кругу). Клиенты подключаются через
Angie и расходятся по инстансам; каждый клиент знает свой инстанс по X-Instance.
Событие публикуется В ОДИН инстанс (rt-sc-a) — как событие, пришедшее
обработчику на нём; дальше его разносит шина или никто.

  fanout <тр> <шина>   — 20 клиентов SSE или WS, 10 событий; шины none, redis, nats.
  longpoll <конфиг>    — 10 клиентов long polling, 20 событий; курсор — в памяти
                         инстанса; конфиги scale (по кругу) и sticky (hash $cookie_sid).
                         Шина redis: события есть на обоих инстансах, и пропуски
                         и дубли — только от курсора.

    python3 scripts/run-scale.py                         → все варианты, фикстура
    python3 scripts/run-scale.py go fanout ws none       → один вариант, строка JSON
    python3 scripts/run-scale.py java longpoll sticky    → один вариант, строка JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, SCALE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "scale.jsonl")
A, B, LB, REDIS, NATS = "rt-sc-a", "rt-sc-b", "rt-sc-angie", "rt-sc-redis", "rt-sc-nats"
SERVERS = ("go", "java")
BUSES = ("none", "redis", "nats")
K_FAN, N_FAN, K_LP, N_LP = 20, 10, 10, 20


def start(server, bus, conf):
    docker.rm(LB, A, B)
    if bus == "redis":
        docker.redis(REDIS)
    if bus == "nats":
        docker.nats(NATS)
    env = {"RT_BUS": bus, "RT_REDIS": REDIS + ":6379", "RT_NATS": "nats://%s:4222" % NATS}
    docker.server(A, server, env)
    docker.server(B, server, env)
    docker.angie(conf, LB)


def stop():
    docker.rm(LB, A, B, REDIS, NATS)


def spread_ok(per_instance):
    return set(per_instance) == {A, B} and min(per_instance.values()) > 0


def fanout(server, transport, bus):
    start(server, bus, "scale")
    r = docker.probe("fanout", "-transport", transport, "-url", "http://" + LB, "-publish", "http://%s:8080" % A,
                     "-topic", docker.topic("sf"), "-clients", str(K_FAN), "-n", str(N_FAN), "-wait", "10s")
    row = {"kind": "fanout", "variant": "%s/fanout/%s/%s" % (server, transport, bus), "probe": "k%d-n%d" % (K_FAN, N_FAN),
           "cmd": "python3 scripts/run-scale.py %s fanout %s %s" % (server, transport, bus),
           "server": server, "transport": transport, "bus": bus, **r}
    if not r["all_connected"] or r["publish_errors"] or not spread_ok(r["clients_per_instance"]):
        row["outcome"] = "error"
        row["detail"] = "опыт не поставлен: подключены не все, публикация не прошла или клиенты на одном инстансе"
    elif r["clients_full"] == K_FAN:
        row["outcome"], row["detail"] = "all", "все %d клиентов получили все %d" % (K_FAN, N_FAN)
    else:
        row["outcome"] = "partial"
        row["detail"] = "все события у %d из %d (по инстансам: %s), без единого — %d" % (
            r["clients_full"], K_FAN, json.dumps(r["full_per_instance"], sort_keys=True), r["clients_none"])
    SCALE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def longpoll(server, conf):
    start(server, "redis", conf)
    r = docker.probe("longpoll", "-url", "http://" + LB, "-publish", "http://%s:8080" % A,
                     "-topic", docker.topic("sl"), "-clients", str(K_LP), "-n", str(N_LP),
                     "-event-interval", "150ms", "-wait", "3s")
    row = {"kind": "longpoll", "variant": "%s/longpoll/%s" % (server, conf), "probe": "k%d-n%d" % (K_LP, N_LP),
           "cmd": "python3 scripts/run-scale.py %s longpoll %s" % (server, conf),
           "server": server, "angie_conf": conf, "bus": "redis", **r}
    if r["publish_errors"]:
        row["outcome"], row["detail"] = "error", "публикация не прошла"
    elif r["missing_total"] == 0 and r["dups_total"] == 0:
        row["outcome"], row["detail"] = "all", "у всех %d клиентов ни пропусков, ни дублей" % K_LP
    else:
        row["outcome"] = "partial"
        row["detail"] = "пропусков %d, дублей %d; чистых клиентов %d из %d" % (
            r["missing_total"], r["dups_total"], r["clients_clean"], K_LP)
    SCALE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def plan():
    for server in SERVERS:
        for transport in ("sse", "ws"):
            for bus in BUSES:
                yield fanout, server, transport, bus
        for conf in ("scale", "sticky"):
            yield longpoll, server, conf


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    args = sys.argv[1:]
    try:
        if args:
            fn = {"fanout": fanout, "longpoll": longpoll}.get(args[1] if len(args) > 1 else "")
            if fn is None or args[0] not in SERVERS:
                raise SystemExit(__doc__)
            row = fn(args[0], *args[2:])
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
            return
        docker.build()
        rows = []
        for fn, *a in plan():
            row = fn(*a)
            rows.append(row)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    finally:
        stop()
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
