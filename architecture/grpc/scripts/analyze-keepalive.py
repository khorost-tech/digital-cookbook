"""Ось 6: разбор. Контроль первым.

Контроль пингов: при разрешённом на сервере интервале стрим доживает — иначе
GOAWAY в политике по умолчанию мог прийти по другой причине. Контроль простоя:
напрямую, без прокси, соединение переживает простой. «Пережило» засчитывается
только при состоянии канала READY перед вторым вызовом.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import KEEPALIVE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "keepalive.jsonl")


def read(path=FIXTURE):
    return KEEPALIVE.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["kind"] == "pings" and r["policy"] == "allowed" and r["outcome"] != "survived":
            raise SystemExit("контроль %s: при разрешённых пингах стрим не дожил" % r["variant"])
        if r["kind"] in ("idle", "silent") and r["path"] == "direct" and r["outcome"] != "survived":
            raise SystemExit("контроль %s: без прокси соединение не пережило простой" % r["variant"])
        if r["kind"] == "idle" and (r["outcome"] == "survived") != r["same_conn"]:
            raise SystemExit("%s: исход %s расходится с адресом соединения" % (r["variant"], r["outcome"]))
        if r["outcome"] == "survived" and r["kind"] == "idle" and r["state_before_second"] != "READY":
            raise SystemExit("%s: «пережило», но канал %s" % (r["variant"], r["state_before_second"]))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-32s %-12s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
