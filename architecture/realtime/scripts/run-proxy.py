"""Ось 2: что прокси делает с длинным соединением → fixtures/proxy.jsonl.

Три опыта, каждый против Go и Java, с контролем без прокси:

  sse-window <путь>  — пять событий SSE; сколько клиент получил за 2 с, пока поток открыт.
                       Пути: direct, buffered (proxy_buffering по умолчанию),
                       buffered-gzip (то же и gzip для text/event-stream), unbuffered.
  handshake <путь>   — рукопожатие WebSocket. Пути: direct, buffered (Upgrade и
                       Connection проксируются), no-upgrade (не проксируются).
  idle <тр> <путь>   — соединение простаивает 6 с при proxy_read_timeout 3 с; затем
                       одно событие. Пути: direct (без прокси, без heartbeat — контроль),
                       short-idle (без heartbeat), short-idle-hb (heartbeat раз в 1 с).

    python3 scripts/run-proxy.py                           → все варианты, фикстура
    python3 scripts/run-proxy.py go sse-window buffered    → один вариант, строка JSON
    python3 scripts/run-proxy.py java idle ws short-idle   → один вариант, строка JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, HANDSHAKE, IDLE, SSE_WINDOW  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "proxy.jsonl")
SRV, PROXY = "rt-px-srv", "rt-px-angie"
SERVERS = ("go", "java")
SSE_PATHS = {"direct": None, "buffered": "buffered", "buffered-gzip": "gzip", "unbuffered": "unbuffered"}
HS_PATHS = {"direct": None, "buffered": "buffered", "no-upgrade": "no-upgrade"}
IDLE_PATHS = {"direct": (None, 0), "short-idle": ("short-idle", 0), "short-idle-hb": ("short-idle", 1000)}
N, WINDOW, IDLE_S = 5, "2s", "6s"


def url(conf):
    return "http://%s" % PROXY if conf else "http://%s:8080" % SRV


def start(server, conf, env=None):
    docker.server(SRV, server, env)
    if conf:
        docker.angie(conf, PROXY)
    else:
        docker.rm(PROXY)


def sse_window(server, path):
    conf = SSE_PATHS[path]
    start(server, conf)
    args = ["sse-window", "-url", url(conf), "-publish", "http://%s:8080" % SRV, "-topic", docker.topic("pw"),
            "-n", str(N), "-window", WINDOW]
    if path == "buffered-gzip":
        args.append("-compress")  # клиент принимает gzip, иначе прокси не сжимает
    r = docker.probe(*args)
    row = {"kind": "sse-window", "variant": "%s/sse-window/%s" % (server, path), "probe": "n%d-window%s" % (N, WINDOW),
           "cmd": "python3 scripts/run-proxy.py %s sse-window %s" % (server, path),
           "server": server, "path": path, "angie_conf": conf, **r}
    if not r["stream_open_at_count"] or r["publish_errors"]:
        row["outcome"], row["detail"] = "error", "поток не открыт или публикация не прошла: опыт не поставлен"
    elif r["received_in_window"] == N:
        row["outcome"], row["detail"] = "delivered", "все %d в окне" % N
    else:
        row["outcome"], row["detail"] = "held", "в окне %d из %d" % (r["received_in_window"], N)
    SSE_WINDOW.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def handshake(server, path):
    conf = HS_PATHS[path]
    start(server, conf)
    r = docker.probe("ws-handshake", "-url", url(conf), "-topic", docker.topic("ph"))
    row = {"kind": "handshake", "variant": "%s/handshake/%s" % (server, path), "probe": "ws-dial",
           "cmd": "python3 scripts/run-proxy.py %s handshake %s" % (server, path),
           "server": server, "path": path, "angie_conf": conf, **r}
    if r["handshake_ok"]:
        row["outcome"], row["detail"] = "upgraded", "рукопожатие прошло"
    elif r["result"].startswith("рукопожатие: статус"):
        row["outcome"], row["detail"] = "refused", r["result"]
    else:
        row["outcome"], row["detail"] = "error", r["result"]
    HANDSHAKE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def idle(server, transport, path):
    conf, hb = IDLE_PATHS[path]
    start(server, conf, {"RT_HEARTBEAT_MS": str(hb)} if hb else None)
    r = docker.probe("idle", "-transport", transport, "-url", url(conf), "-publish", "http://%s:8080" % SRV,
                     "-topic", docker.topic("pi"), "-idle", IDLE_S)
    row = {"kind": "idle", "variant": "%s/idle/%s/%s" % (server, transport, path), "probe": "idle%s" % IDLE_S,
           "cmd": "python3 scripts/run-proxy.py %s idle %s %s" % (server, transport, path),
           "server": server, "transport": transport, "path": path, "angie_conf": conf, "heartbeat_ms": hb, **r}
    if r["publish_errors"]:
        row["outcome"], row["detail"] = "error", "публикация не прошла"
    elif not r["closed_during_idle"] and r["event_after_idle"]:
        row["outcome"], row["detail"] = "alive", "после простоя событие дошло"
    else:
        row["outcome"] = "closed"
        row["detail"] = "закрыто во время простоя: %s" % (r["closed_by"] or "без кода")
    IDLE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


def plan():
    for server in SERVERS:
        for path in SSE_PATHS:
            yield (sse_window, server, path)
        for path in HS_PATHS:
            yield (handshake, server, path)
        for transport in ("ws", "sse"):
            for path in IDLE_PATHS:
                yield (idle, server, transport, path)


SHOW = ("variant", "outcome", "detail")


def main():
    docker.network()
    args = sys.argv[1:]
    try:
        if args:
            fn = {"sse-window": sse_window, "handshake": handshake, "idle": idle}.get(args[1] if len(args) > 1 else "")
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
        docker.rm(PROXY, SRV)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
