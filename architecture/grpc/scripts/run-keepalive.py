"""Ось 6: keepalive → fixtures/keepalive.jsonl.

  pings <политика>       — без прокси. Клиент пингует раз в 10 с (меньше клиенты
                           Go и Java не позволяют) во время долгого серверного стрима,
                           который молчит: сообщение в начале и через 50 с. Политики сервера: default —
                           как у библиотеки (не чаще раза в 5 минут); allowed —
                           GRPC_KA_MIN_TIME_MS=5000. Все пары.
  idle <путь> <ka>       — вызов, простой 20 с, вызов; таймаут простоя у прокси — 15 с.
                           Пути: direct (контроль), angie-l4, envoy-l4, angie-l7,
                           envoy-l7. ka: off — без пингов; on — пинги раз в 10 с и без
                           вызовов, сервер их разрешает (GRPC_KA_MIN_TIME_MS=5000,
                           GRPC_KA_PERMIT_NO_STREAM=1). Пережило ли соединение
                           простой, решает локальный адрес TCP-соединения клиента:
                           тот же у обоих вызовов — пережило, разный — новое.
                           Состояние канала перед вторым вызовом — второстепенный
                           признак: READY бывает и после переподключения.

  silent <путь> <ka>     — активный серверный стрим молчит 20 с (сообщение в начале и
                           через 20 с) при таймауте простоя прокси 15 с. Таймаут
                           соединения без вызовов к открытому вызову не применяется;
                           молчание внутри вызова ограничивают другие параметры
                           (Angie grpc_read_timeout 60 с, Envoy stream_idle_timeout
                           5 минут), а L4-прокси видит только тишину в TCP.

    python3 scripts/run-keepalive.py                         → все варианты, фикстура
    python3 scripts/run-keepalive.py go java pings default   → один вариант, строка JSON
    python3 scripts/run-keepalive.py java idle envoy-l7 on   → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, KEEPALIVE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "keepalive.jsonl")
BACKENDS = ("grpc-bl-1", "grpc-bl-2", "grpc-bl-3")
# Сервер сбрасывает счёт «штрафных» пингов, когда сам отправляет данные, —
# поэтому стрим молчит: сообщение в начале и через 50 с (найдено на первой
# пробе: стрим с сообщением раз в секунду GOAWAY не получает вовсе).
COUNT, INTERVAL_MS = 2, 50000
PATHS = ("direct", "angie-l4", "envoy-l4", "angie-l7", "envoy-l7")


def pings(cl, srv, policy):
    name = "grpc-ka-srv"
    docker.server(name, srv, {"GRPC_KA_MIN_TIME_MS": "5000"} if policy == "allowed" else {})
    try:
        r = docker.client(cl, "keepalive", "-target", name + ":9090", "-ka-time-ms", "10000",
                          "-count", str(COUNT), "-interval-ms", str(INTERVAL_MS), "-duration", "70s", timeout=120)
    finally:
        docker.rm(name)
    row = {"kind": "pings", "variant": "%s->%s/pings/%s" % (cl, srv, policy), "probe": "ka10s-stream%ds" % COUNT,
           "cmd": "python3 scripts/run-keepalive.py %s %s pings %s" % (cl, srv, policy), "client": cl,
           "server": srv, "policy": policy, "code": r["code"], "message": r["message"], "received": r["received"],
           "stream_interval_ms": INTERVAL_MS}
    if r["code"] == "OK" and r["received"] == COUNT:
        row["outcome"], row["detail"] = "survived", "стрим дожил: получено %d из %d" % (COUNT, COUNT)
    elif "too_many_pings" in r["message"] or "ENHANCE_YOUR_CALM" in r["message"]:
        row["outcome"] = "goaway"
        row["detail"] = "GOAWAY too_many_pings после %d сообщений из %d: %s" % (r["received"], COUNT, r["code"])
    else:
        row["outcome"] = "error"
        row["detail"] = "%s после %d сообщений: %s" % (r["code"], r["received"], r["message"][:100])
    KEEPALIVE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def idle(cl, path, ka):
    env = {"GRPC_KA_MIN_TIME_MS": "5000", "GRPC_KA_PERMIT_NO_STREAM": "1"} if ka == "on" else {}
    for b in BACKENDS:
        docker.server(b, "go", env)
    prx = None
    try:
        if path == "direct":
            target = BACKENDS[0] + ":9090"
        else:
            prx = docker.proxy(*path.split("-"))
            target = prx + ":9000"
        args = ["idle", "-target", target, "-idle", "20s"]
        if ka == "on":
            args += ["-ka-time-ms", "10000", "-permit"]
        r = docker.client(cl, *args, timeout=120)
    finally:
        docker.rm(*BACKENDS, *([prx] if prx else []))
    row = {"kind": "idle", "variant": "%s/idle/%s/ka-%s" % (cl, path, ka), "probe": "idle20s-timeout15s",
           "cmd": "python3 scripts/run-keepalive.py %s idle %s %s" % (cl, path, ka), "client": cl, "path": path,
           "ka": ka, "first": r["first"], "second": r["second"], "second_message": r["second_message"],
           "state_before_second": r["state_before_second"], "same_conn": r["first_local"] == r["second_local"]}
    if r["first"] != "OK":
        row["outcome"], row["detail"] = "error", "первый вызов: %s" % r["first"]
    elif not r["first_local"] or not r["second_local"]:
        row["outcome"], row["detail"] = "error", "проба не узнала локальный адрес соединения"
    elif r["second"] == "OK" and row["same_conn"]:
        row["outcome"] = "survived"
        row["detail"] = "соединение пережило простой: оба вызова по одному соединению (канал %s)" % r["state_before_second"]
    elif r["second"] == "OK":
        row["outcome"] = "reconnected"
        row["detail"] = "соединение закрыто на простое, второй вызов — по новому (канал перед ним %s)" % (
            r["state_before_second"])
    else:
        row["outcome"] = "error"
        row["detail"] = "второй вызов: %s (канал %s) %s" % (r["second"], r["state_before_second"], r["second_message"][:80])
    KEEPALIVE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def silent(cl, path, ka):
    env = {"GRPC_KA_MIN_TIME_MS": "5000", "GRPC_KA_PERMIT_NO_STREAM": "1"} if ka == "on" else {}
    for b in BACKENDS:
        docker.server(b, "go", env)
    prx = None
    try:
        if path == "direct":
            target = BACKENDS[0] + ":9090"
        else:
            prx = docker.proxy(*path.split("-"))
            target = prx + ":9000"
        args = ["keepalive", "-target", target, "-count", "2", "-interval-ms", "20000", "-duration", "40s"]
        if ka == "on":
            args += ["-ka-time-ms", "10000", "-permit"]
        r = docker.client(cl, *args, timeout=120)
    finally:
        docker.rm(*BACKENDS, *([prx] if prx else []))
    row = {"kind": "silent", "variant": "%s/silent/%s/ka-%s" % (cl, path, ka), "probe": "stream-silent20s-timeout15s",
           "cmd": "python3 scripts/run-keepalive.py %s silent %s %s" % (cl, path, ka), "client": cl, "path": path,
           "ka": ka, "code": r["code"], "message": r["message"], "received": r["received"]}
    if r["code"] == "OK" and r["received"] == 2:
        row["outcome"], row["detail"] = "survived", "молчащий стрим дожил: получены оба сообщения"
    elif r["received"] == 1:
        row["outcome"] = "dropped"
        row["detail"] = "стрим оборван на молчании: %s после 1 сообщения" % r["code"]
    else:
        row["outcome"], row["detail"] = "error", "%s после %d сообщений: %s" % (r["code"], r["received"], r["message"][:80])
    KEEPALIVE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    a = sys.argv[1:]
    if a:
        if len(a) == 4 and a[2] == "pings":
            row = pings(a[0], a[1], a[3])
        else:
            row = (silent if a[1] == "silent" else idle)(a[0], a[2], a[3])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for (cl, srv), policy in itertools.product(itertools.product(docker.KINDS, repeat=2), ("default", "allowed")):
        rows.append(pings(cl, srv, policy))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    for cl, path, ka in itertools.product(docker.KINDS, PATHS, ("off", "on")):
        rows.append(idle(cl, path, ka))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    for cl, path, ka in itertools.product(docker.KINDS, PATHS, ("off", "on")):
        rows.append(silent(cl, path, ka))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
