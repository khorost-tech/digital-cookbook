"""Ось 5: встроенные ретраи из service config → fixtures/retry.jsonl.

Клиент подключается с service config, сервер отказывает первым fail_times
попыткам с заданным кодом. Проба своих повторов не делает; попытки считает
сервер (счётчик по ключу), номер успешной попытки — из
grpc-previous-rpc-attempts. Все пары.

Варианты:
  no-policy    — контроль: без политики, отказ UNAVAILABLE дважды;
  unavailable  — maxAttempts 4 на UNAVAILABLE, отказ дважды;
  internal     — та же политика, отказ INTERNAL дважды (не в retryableStatusCodes);
  max10        — maxAttempts 10, отказ 7 раз (предел библиотек — 5 попыток);
  deadline     — maxAttempts 4, первая пауза 1 с, отказ трижды, срок вызова 500 мс;
                 пауза перед повтором — initialBackoff с разбросом ±20% (0,8–1,2 с
                 в grpc-go и grpc-java 1.84), длиннее срока: вторая попытка не успевает.
Стрим (граница повторов): сервер отвечает UNAVAILABLE до первого сообщения
(stream-before) или после первого (stream-after); политика maxAttempts 4;
контроль stream-no-policy. Отказ до первого сообщения идёт без заголовков
ответа (trailers-only); stream-headers — сервер сначала отправляет заголовки,
потом UNAVAILABLE, не отправив ни одного сообщения (GRPC_STREAM_HEADERS_FIRST=1).
Повторы считает сервер — число вызовов стрима.

    python3 scripts/run-retry.py                    → все варианты, фикстура
    python3 scripts/run-retry.py go java max10      → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, RETRY  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "retry.jsonl")


def policy(max_attempts, initial="0.1s"):
    return json.dumps({"methodConfig": [{"name": [{"service": "demo.v1.Demo"}], "retryPolicy": {
        "maxAttempts": max_attempts, "initialBackoff": initial, "maxBackoff": "2s", "backoffMultiplier": 2,
        "retryableStatusCodes": ["UNAVAILABLE"]}}]}, separators=(",", ":"))


# вариант → (service config или "", код отказа, сколько отказов, срок вызова мс)
VARIANTS = {
    "no-policy": ("", "UNAVAILABLE", 2, 0),
    "unavailable": (policy(4), "UNAVAILABLE", 2, 0),
    "internal": (policy(4), "INTERNAL", 2, 0),
    "max10": (policy(10), "UNAVAILABLE", 7, 0),
    "deadline": (policy(4, "1s"), "UNAVAILABLE", 3, 500),
}


# вариант → (service config или "", после скольких сообщений отказ, заголовки до отказа)
STREAM_VARIANTS = {"stream-no-policy": ("", 0, False), "stream-before": (policy(4), 0, False),
                   "stream-headers": (policy(4), 0, True), "stream-after": (policy(4), 1, False)}


def measure_stream(cl, srv, name):
    cfg, fail_after, headers = STREAM_VARIANTS[name]
    srvname = "grpc-rt-srv"
    docker.server(srvname, srv, {"GRPC_STREAM_FAIL_AFTER": str(fail_after),
                                 "GRPC_STREAM_HEADERS_FIRST": "1" if headers else "0"})
    try:
        args = ["stream-retry", "-target", srvname + ":9090", "-count", "3"]
        if cfg:
            args += ["-retry", cfg]
        r = docker.client(cl, *args)
        st = docker.stats(srvname)
    finally:
        docker.rm(srvname)
    row = {"kind": "stream-retry", "variant": "%s->%s/%s" % (cl, srv, name), "probe": "fail-after%d%s" % (fail_after, "-headers" if headers else ""),
           "cmd": "python3 scripts/run-retry.py %s %s %s" % (cl, srv, name), "client": cl, "server": srv,
           "name": name, "service_config": cfg, "fail_after": fail_after, "headers_first": headers, "server_attempts": st["stream_calls"],
           "client_code": r["code"], "received": r["received"], "success_attempt": 0, "outcome": "observed"}
    row["detail"] = "вызовов стрима на сервере %d, клиент получил %s после %d сообщений" % (
        st["stream_calls"], r["code"], r["received"])
    RETRY.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def measure(cl, srv, name):
    if name in STREAM_VARIANTS:
        return measure_stream(cl, srv, name)
    cfg, code, times, deadline = VARIANTS[name]
    srvname = "grpc-rt-srv"
    docker.server(srvname, srv)
    try:
        args = ["unary", "-target", srvname + ":9090", "-mode", "fail", "-key", "r", "-fail-code", code,
                "-fail-times", str(times)]
        if cfg:
            args += ["-retry", cfg]
        if deadline:
            args += ["-deadline-ms", str(deadline)]
        r = docker.client(cl, *args)
        st = docker.stats(srvname)
    finally:
        docker.rm(srvname)
    attempts = st["attempts"].get("r", 0)
    row = {"kind": "retry", "variant": "%s->%s/%s" % (cl, srv, name), "probe": "fail%d-%s" % (times, code),
           "cmd": "python3 scripts/run-retry.py %s %s %s" % (cl, srv, name), "client": cl, "server": srv,
           "name": name, "service_config": cfg, "fail_code": code, "fail_times": times, "deadline_ms": deadline,
           "server_attempts": attempts, "client_code": r["code"], "success_attempt": r.get("attempt", 0),
           "outcome": "observed"}
    row["detail"] = "попыток на сервере %d, клиент получил %s" % (attempts, r["code"])
    if attempts == 0:
        row["outcome"], row["detail"] = "error", "до сервера не дошло ни одной попытки"
    RETRY.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    if sys.argv[1:]:
        row = measure(*sys.argv[1:4])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for (cl, srv), name in itertools.product(itertools.product(docker.KINDS, repeat=2),
                                             list(VARIANTS) + list(STREAM_VARIANTS)):
        rows.append(measure(cl, srv, name))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
