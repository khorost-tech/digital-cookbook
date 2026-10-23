"""Ось 5: медленный клиент → fixtures/slow.jsonl.

Рядом два клиента одной темы: быстрый и медленный (приёмный буфер 4 КБ, пауза
5 мс после каждого события). Издатель отправляет 3000 событий по 1 КБ подряд —
заведомо больше, чем помещается в буферы сокетов, иначе очередь сервера «не
растёт» из-за ядра, а не из-за политики. Счёт снимается со стороны сервера
(/debug/stats): наибольшая очередь соединения, выброшено, отключено.

Политики очереди (RT_QUEUE, длина 256): unbounded, drop-oldest, disconnect.
Пути: direct; buffered — через Angie с буферизацией по умолчанию; unbuffered —
через Angie без неё.

    python3 scripts/run-slow.py                                  → все варианты, фикстура
    python3 scripts/run-slow.py go sse disconnect buffered       → один вариант, строка JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, SLOW  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "slow.jsonl")
SRV, PROXY = "rt-px-srv", "rt-px-angie"   # имена из конфигов angie/*.conf
SERVERS = ("go", "java")
TRANSPORTS = ("sse", "ws")
POLICIES = ("unbounded", "drop-oldest", "disconnect")
PATHS = {"direct": None, "buffered": "buffered", "unbuffered": "unbuffered"}
N, PAYLOAD, QUEUE_LEN, GREW = 3000, 1024, 256, 100


def measure(server, transport, policy, path):
    conf = PATHS[path]
    docker.server(SRV, server, {"RT_QUEUE": policy, "RT_QUEUE_LEN": str(QUEUE_LEN)})
    if conf:
        docker.angie(conf, PROXY)
    else:
        docker.rm(PROXY)
    url = "http://%s" % PROXY if conf else "http://%s:8080" % SRV
    r = docker.probe("slow", "-transport", transport, "-url", url, "-publish", "http://%s:8080" % SRV,
                     "-topic", docker.topic("sw"), "-n", str(N), "-payload", str(PAYLOAD),
                     "-read-delay", "5ms", "-rcvbuf", "4096", "-wait", "30s")
    # /debug/stats пробы шёл на url клиента: через прокси — тот же сервер, но
    # счёт снимаем у сервера напрямую, чтобы не зависеть от пути.
    stats = json.loads(docker.sh("docker", "exec", SRV, "wget", "-qO-", "http://127.0.0.1:8080/debug/stats"))
    row = {"kind": "slow", "variant": "%s/%s/%s/%s" % (server, transport, policy, path),
           "probe": "n%d-p%d" % (N, PAYLOAD),
           "cmd": "python3 scripts/run-slow.py %s %s %s %s" % (server, transport, policy, path),
           "server": server, "transport": transport, "policy": policy, "path": path, "angie_conf": conf,
           "queue_len": QUEUE_LEN, "n": N, "payload": PAYLOAD, "probe_result": r,
           "max_queue": stats["max_queue"], "dropped": stats["dropped"], "disconnected": stats["disconnected"],
           "fast_all": r["fast_all"], "fast_missing": r["fast_missing"], "slow_received": r["slow_received"]}
    if r["publish_errors"] or not r["fast_all"] or r["fast_missing"]:
        row["outcome"], row["detail"] = "error", "контроль: быстрый клиент получил не всё — опыт сломан"
    elif stats["disconnected"]:
        row["outcome"], row["detail"] = "disconnected", "медленный отключён сервером (очередь до %d)" % stats["max_queue"]
    elif stats["dropped"]:
        row["outcome"], row["detail"] = "dropped", "сервер выбросил %d событий (очередь до %d)" % (
            stats["dropped"], stats["max_queue"])
    elif stats["max_queue"] >= GREW:
        row["outcome"], row["detail"] = "grew", "очередь на сервере выросла до %d" % stats["max_queue"]
    else:
        row["outcome"], row["detail"] = "unnoticed", "сервер не заметил медленного: очередь не выше %d" % stats["max_queue"]
    SLOW.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    args = sys.argv[1:]
    try:
        if args:
            if len(args) != 4 or args[0] not in SERVERS or args[1] not in TRANSPORTS or args[2] not in POLICIES \
                    or args[3] not in PATHS:
                raise SystemExit(__doc__)
            row = measure(*args)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
            return
        docker.build()
        rows = []
        for server in SERVERS:
            for transport in TRANSPORTS:
                for policy in POLICIES:
                    for path in PATHS:
                        row = measure(server, transport, policy, path)
                        rows.append(row)
                        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    finally:
        docker.rm(PROXY, SRV)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
