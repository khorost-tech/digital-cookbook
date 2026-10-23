"""Ось 1: цена события на проводе → fixtures/wire.jsonl.

Клиент подключается к серверу через считающий TCP-прокси, издатель публикует
мимо прокси — в счёт идут только байты транспорта клиента. Цена события —
РАЗНОСТЬЮ двух прогонов (N2 − N1 событий), а не делением: рукопожатие и
заголовки потока одинаковы в обоих прогонах и в разности сокращаются.

События — раз в 100 мс, полезная нагрузка 64 байта, тема — имя одной длины.

    python3 scripts/run-wire.py                    → все варианты, фикстура
    python3 scripts/run-wire.py <сервер> <вариант> → один вариант, строка JSON
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import docker  # noqa: E402
from scripts.outcome import END_MARKER, WIRE  # noqa: E402

FIXTURE = os.path.join(docker.HERE, "fixtures", "wire.jsonl")
N1, N2, PAYLOAD, EVENT_MS = 20, 120, 64, 100

# вариант → (транспорт, интервал опроса мс, сжатие, env сервера)
VARIANTS = {
    "sse": ("sse", 0, False, {}),
    "ws": ("ws", 0, False, {}),
    "longpoll": ("longpoll", 0, False, {}),
    "poll-50ms": ("poll", 50, False, {}),      # опрос чаще событий: пустые ответы
    "poll-500ms": ("poll", 500, False, {}),    # опрос реже событий: пачки
    "sse-gzip": ("sse", 0, True, {"RT_SSE_GZIP": "1"}),
    # Уровень 9: compress/flate Go на уровне по умолчанию при сбросе после каждого
    # события почти не находит повторов (probes/flate); Java уровень не читает.
    "sse-gzip-9": ("sse", 0, True, {"RT_SSE_GZIP": "1", "RT_SSE_GZIP_LEVEL": "9"}),
    "ws-deflate": ("ws", 0, True, {"RT_WS_COMPRESS": "1"}),
    # Порог сжатия 1 байт: у coder/websocket по умолчанию короткие сообщения не
    # сжимаются; Java (Netty) порога не имеет, и строка у неё совпадает с ws-deflate.
    "ws-deflate-all": ("ws", 0, True, {"RT_WS_COMPRESS": "1", "RT_WS_COMPRESS_THRESHOLD": "1"}),
}
# Опрос — по таймеру: сколько опросов придётся на прогон, зависит от того, как
# легли таймеры клиента и издателя. Счёт этих вариантов недетерминирован
# (замечено на первом прогоне оси 1, до сводок сессий) и в сравнение сессий не входит.
NONDETERMINISTIC = {"poll-50ms", "poll-500ms"}
SERVERS = ("go", "java")


def measure(server, name):
    transport, poll_ms, compress, env = VARIANTS[name]
    srv = docker.server("rt-wire-" + server, server, env)
    prx = docker.proxy("rt-wire-proxy", srv + ":8080")
    runs = {}
    try:
        for n in (N1, N2):
            # Тема — одна и та же от сессии к сессии: её имя входит в каждое событие,
            # и случайные цифры меняли бы байты сжатых вариантов. Своя на каждый
            # из двух прогонов: сервер помнит недавние события темы.
            args = ["wire", "-transport", transport, "-topic", "wire-%09d" % n,
                    "-url", "http://%s:9000" % prx, "-publish", "http://%s:8080" % srv,
                    "-control", "http://%s:9100" % prx, "-n", str(n), "-payload", str(PAYLOAD),
                    "-event-interval", "%dms" % EVENT_MS, "-wait", "15s"]
            if poll_ms:
                args += ["-poll-interval", "%dms" % poll_ms]
            if compress:
                args.append("-compress")
            runs[str(n)] = docker.probe(*args)
    finally:
        docker.rm(prx, srv)
    a, b = runs[str(N1)], runs[str(N2)]
    k = N2 - N1
    row = {"kind": "wire", "variant": "%s/%s" % (server, name), "probe": "n%d-n%d" % (N1, N2),
           "cmd": "python3 scripts/run-wire.py %s %s" % (server, name),
           "server": server, "name": name, "transport": transport, "compress": compress,
           "poll_interval_ms": poll_ms, "event_interval_ms": EVENT_MS, "payload": PAYLOAD,
           "down_per_event": round((b["bytes_down"] - a["bytes_down"]) / k, 1),
           "up_per_event": round((b["bytes_up"] - a["bytes_up"]) / k, 1),
           "requests_per_event": round((b["requests"] - a["requests"]) / k, 2),
           "empty_per_event": round((b["empty"] - a["empty"]) / k, 2),
           "nondeterministic": name in NONDETERMINISTIC, "runs": runs}
    complete = all(r["all_received"] and r["missing"] == 0 and r["dups"] == 0 and r["publish_errors"] == 0
                   for r in runs.values())
    if complete:
        row["outcome"], row["detail"] = "delivered", "доставлены все %d и %d" % (N1, N2)
    else:
        row["outcome"] = "incomplete"
        row["detail"] = "; ".join("n=%s: получено %d, пропусков %d, дублей %d" % (
            n, r["received"], r["missing"], r["dups"]) for n, r in runs.items())
    WIRE.parse_line(json.dumps(row, ensure_ascii=False))
    return row


SHOW = ("variant", "outcome", "down_per_event", "up_per_event", "requests_per_event", "empty_per_event")


def main():
    docker.network()
    if sys.argv[1:]:
        if len(sys.argv) != 3 or sys.argv[1] not in SERVERS or sys.argv[2] not in VARIANTS:
            raise SystemExit("использование: run-wire.py {%s} {%s}" % ("|".join(SERVERS), "|".join(VARIANTS)))
        row = measure(sys.argv[1], sys.argv[2])
        print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False))
        return
    docker.build()
    rows = []
    for server in SERVERS:
        for name in VARIANTS:
            row = measure(server, name)
            rows.append(row)
            print(json.dumps({k: row[k] for k in SHOW}, ensure_ascii=False), flush=True)
    with open(FIXTURE, "w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
        f.write(END_MARKER + "\n")


if __name__ == "__main__":
    main()
