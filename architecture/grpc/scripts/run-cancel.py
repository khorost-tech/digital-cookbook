"""Ось 2: отмена серверного стрима → fixtures/cancel.jsonl.

Сервер шлёт 20 сообщений с паузой 100 мс; клиент читает 3 и отменяет вызов.
Сервер либо проверяет отмену перед каждой отправкой (GRPC_STREAM_CHECK_CANCEL=1),
либо нет. Счёт — на сервере, а не по клиенту: сколько попыток отправки он
сделал после того, как заметил отмену, сколько из них прошли и сколько вернули
ошибку. Контроль — клиент читает все 20 без отмены. Все четыре пары.

Только сервер Java — где идёт цикл отправки без проверки отмены
(GRPC_STREAM_SEND): no-check — внутри обработчика; async — в отдельной задаче
после возврата обработчика; async-handler — то же с setOnCancelHandler;
sync-handler — внутри обработчика с setOnCancelHandler. grpc-java доставляет
отмену приложению callback'ом, а callback ждёт возврата обработчика.

    python3 scripts/run-cancel.py                       → все варианты, фикстура
    python3 scripts/run-cancel.py go java check         → один вариант, строка JSON
"""

import itertools
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import CANCEL, END_MARKER  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "cancel.jsonl")
COUNT, INTERVAL, READ = 20, 100, 3
SERVER_MODES = ("check", "no-check", "control")
JAVA_MODES = ("async", "async-handler", "sync-handler")  # только сервер Java
SEND = {"async": "async", "async-handler": "async-handler", "sync-handler": "sync-handler"}


def modes(srv):
    return SERVER_MODES + (JAVA_MODES if srv == "java" else ())


def measure(cl, srv, mode):
    name = "grpc-cn-srv"
    if mode in JAVA_MODES and srv != "java":
        raise SystemExit("режим %s — только для сервера java" % mode)
    docker.server(name, srv, {"GRPC_STREAM_CHECK_CANCEL": "1" if mode == "check" else "0",
                              "GRPC_STREAM_SEND": SEND.get(mode, "sync")})
    try:
        read = COUNT if mode == "control" else READ
        r = docker.client(cl, "cancel", "-target", name + ":9090", "-count", str(COUNT),
                          "-interval-ms", str(INTERVAL), "-read", str(read))
        st = docker.stats(name)
    finally:
        docker.rm(name)
    after = st["stream_sent_after_cancel"] + st["stream_send_errors"]
    row = {"kind": "cancel", "variant": "%s->%s/%s" % (cl, srv, mode), "probe": "count%d-read%d" % (COUNT, read),
           "cmd": "python3 scripts/run-cancel.py %s %s %s" % (cl, srv, mode),
           "client": cl, "server": srv, "mode": mode, "received": r["received"], **{k: st[k] for k in (
               "stream_sent", "stream_sent_after_cancel", "stream_send_errors", "stream_cancel_seen")}}
    if srv == "java":
        row.update({k: st[k] for k in ("cancel_handler_calls", "cancel_handler_before_end")})
    if mode == "control":
        ok = r["received"] == COUNT and st["stream_cancel_seen"] == 0
        row["outcome"] = "stopped" if ok else "error"
        row["detail"] = "без отмены: получены все %d" % COUNT if ok else "контроль сломан: получено %d" % r["received"]
    elif r["received"] != READ or not st["stream_cancel_seen"]:
        row["outcome"], row["detail"] = "error", "отмена не поставлена: получено %d, сервер отмены не видел" % r["received"]
    elif after == 0:
        row["outcome"], row["detail"] = "stopped", "после отмены сервер не пытался отправлять"
    else:
        row["outcome"] = "kept-sending"
        # «Без ошибки» — вызов отправки вернулся нормально; в сеть по отменённому
        # стриму не уходит ничего, но цикл об отмене так и не узнаёт.
        row["detail"] = "после отмены попыток отправки %d: вернулись без ошибки %d, с ошибкой %d" % (
            after, st["stream_sent_after_cancel"], st["stream_send_errors"])
        if mode.endswith("-handler"):
            row["detail"] += "; обработчик отмены вызван %d раз, %s" % (
                st["cancel_handler_calls"], "до конца цикла" if st["cancel_handler_before_end"] else "после конца цикла")
    CANCEL.parse_line(json.dumps(row, ensure_ascii=False))
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
    for (cl, srv) in itertools.product(docker.KINDS, repeat=2):
        for mode in modes(srv):
            row = measure(cl, srv, mode)
            rows.append(row)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
