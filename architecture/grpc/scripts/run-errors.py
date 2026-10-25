"""Оси 3 и 8: коды ошибок и gRPC-Gateway → fixtures/errors.jsonl.

  errors <режим>   — Unary с ошибкой заданного вида; все четыре пары клиент-сервер.
                     Режимы: ok (контроль), plain (обычная ошибка языка), status
                     (status с кодом NotFound), details (то же с ErrorInfo), panic
                     (паника / исключение, без перехватчика), panic-recover (то же
                     с перехватчиком, GRPC_RECOVER=1). После вызова — жив ли сервер:
                     и контейнер, и отдельный health-вызов.
  gateway <режим>  — тот же сервер за gRPC-Gateway: HTTP-статус, код и детали в
                     теле. Режимы: ok, status, details, invalid, unavailable,
                     plain, panic-recover, stream (серверный стрим через фасад),
                     timeout (Chain на 10 шагов по 100 мс с Grpc-Timeout: 300m),
                     details-registered (details через шлюз с пустым импортом
                     errdetails — без него шлюз не разбирает детали и отвечает 500).

    python3 scripts/run-errors.py                        → все варианты, фикстура
    python3 scripts/run-errors.py go java errors panic   → один вариант, строка JSON
    python3 scripts/run-errors.py go gateway details     → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, ERRORS  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "errors.jsonl")
ERROR_MODES = ("ok", "plain", "status", "details", "panic", "panic-recover")
GATEWAY_MODES = ("ok", "status", "details", "details-registered", "invalid", "unavailable", "plain", "panic-recover",
                 "stream", "timeout")
SRV, GW = "grpc-er-srv", "grpc-er-gw"


def errors(cl, srv, mode):
    docker.server(SRV, srv, {"GRPC_RECOVER": "1" if mode == "panic-recover" else "0"})
    try:
        r = docker.client(cl, "unary", "-target", SRV + ":9090", "-mode", mode.replace("-recover", ""))
        running = docker.alive(SRV)
    finally:
        docker.rm(SRV)
    row = {"kind": "errors", "variant": "%s->%s/%s" % (cl, srv, mode), "probe": "unary",
           "cmd": "python3 scripts/run-errors.py %s %s errors %s" % (cl, srv, mode),
           "client": cl, "server": srv, "mode": mode, "code": r["code"], "message": r["message"],
           "details_reason": r["details_reason"], "health_after": r["alive_after"], "container_running": running,
           "outcome": "observed"}
    row["detail"] = "код %s%s; сервер %s" % (
        r["code"], ", детали %s" % r["details_reason"] if r["details_reason"] else "",
        "жив" if running and r["alive_after"] else "упал")
    if mode == "ok" and (r["code"] != "OK" or not running):
        row["outcome"], row["detail"] = "error", "контроль сломан: %s" % r["code"]
    ERRORS.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def gateway(srv, mode):
    docker.server(SRV, srv, {"GRPC_RECOVER": "1" if mode == "panic-recover" else "0"})
    docker.gateway(GW, SRV + ":9090", "gateway-details" if mode == "details-registered" else "gateway")
    try:
        args = ["rest", "-url", "http://%s:8080" % GW]
        if mode == "stream":
            args += ["-path", "stream", "-count", "3"]
        elif mode == "timeout":
            args += ["-path", "chain", "-steps", "10", "-step-ms", "100", "-grpc-timeout", "300m"]
        else:
            args += ["-mode", mode.replace("-recover", "").replace("-registered", "")]
        r = docker.client("go", *args)
        if mode == "timeout":
            import time
            time.sleep(1.5)  # цикл исполнителя заведомо закончился
        st = docker.stats(SRV)
    finally:
        docker.rm(GW, SRV)
    row = {"kind": "gateway", "variant": "%s/gateway/%s" % (srv, mode), "probe": "rest",
           "cmd": "python3 scripts/run-errors.py %s gateway %s" % (srv, mode), "server": srv, "mode": mode,
           "http_status": r["http_status"], "grpc_code": r.get("grpc_code", ""),
           "details_in_body": r.get("details_in_body", 0), "body_reason": r.get("body_reason", ""),
           "lines": r.get("lines", 0), "first_line": r.get("first_line", ""),
           "steps_done": st["steps_done"], "outcome": "observed"}
    if mode == "stream":
        row["detail"] = "HTTP %d, строк в теле %d" % (r["http_status"], row["lines"])
    elif mode == "timeout":
        row["detail"] = "HTTP %d (%s), исполнитель сделал шагов %d из 10" % (
            r["http_status"], row["grpc_code"], st["steps_done"])
    else:
        row["detail"] = "HTTP %d%s%s" % (r["http_status"], " (%s)" % row["grpc_code"] if row["grpc_code"] else "",
                                          ", в теле %s" % row["body_reason"] if row["body_reason"] else "")
    if mode == "ok" and r["http_status"] != 200:
        row["outcome"], row["detail"] = "error", "контроль сломан: HTTP %d" % r["http_status"]
    ERRORS.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    args = sys.argv[1:]
    if args:
        row = errors(args[0], args[1], args[3]) if args[2] == "errors" else gateway(args[0], args[2])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for (cl, srv), mode in itertools.product(itertools.product(docker.KINDS, repeat=2), ERROR_MODES):
        rows.append(errors(cl, srv, mode))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    for srv, mode in itertools.product(docker.KINDS, GATEWAY_MODES):
        rows.append(gateway(srv, mode))
        print(json.dumps({k: rows[-1][k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
