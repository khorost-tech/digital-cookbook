"""Ось 2: разбор. Контроль первым.

Контроль — строки без прокси: SSE в окне доставлен, рукопожатие прошло,
соединение без heartbeat пережило простой. Иначе ломается сам опыт, а не прокси.
Строка SSE считается только при открытом потоке в момент счёта: иначе прокси
мог отдать буфер при закрытии, и «доставлено» было бы ложью.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import HANDSHAKE, IDLE, SSE_WINDOW  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "proxy.jsonl")
CONTRACTS = {"sse-window": SSE_WINDOW, "handshake": HANDSHAKE, "idle": IDLE}


def read(path=FIXTURE):
    import json
    rows = []
    for line in open(path, encoding="utf-8"):
        if line.startswith("{"):
            row = json.loads(line)
            CONTRACTS[row["kind"]].parse_line(line)
            rows.append(row)
    if not rows or open(path, encoding="utf-8").read().strip().splitlines()[-1] != SSE_WINDOW.END_MARKER:
        raise SystemExit("%s: нет строк или маркера завершения" % path)
    return rows


CONTROL = {"sse-window": "delivered", "handshake": "upgraded", "idle": "alive"}


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: опыт не поставлен — %s" % (r["variant"], r["detail"]))
        if r["path"] == "direct" and r["outcome"] != CONTROL[r["kind"]]:
            raise SystemExit("контроль %s: %s — опыт сломан без прокси" % (r["variant"], r["outcome"]))
        if r["kind"] == "sse-window" and not r["stream_open_at_count"]:
            raise SystemExit("%s: поток закрыт к моменту счёта" % r["variant"])
        if r["kind"] == "idle" and r["outcome"] == "closed" and not r["closed_during_idle"]:
            raise SystemExit("%s: «закрыто», но не во время простоя" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-34s %-9s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
