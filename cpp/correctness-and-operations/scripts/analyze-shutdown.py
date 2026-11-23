"""Ось 3: разбор завершения по SIGTERM. Контроль первым.

Контроль: без сигнала дослужены все — и маленькие ответы, и большой
медленному читателю, — иначе мерили бы поломку, а не
завершение. Строка с сигналом, при котором в полёте никого не было, не
принимается: «дослужены все» на пустых данных ничего не значит. Баланс
served + dropped == sent сверяется у каждой строки.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import SERVED  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "shutdown.jsonl")


def read(path=FIXTURE):
    return SERVED.read_fixture(path)


CONTROLS = {"control-no-signal", "control-slow-reader", "control-slow-reader-halfclose"}


def controls(rows):
    found = [r for r in rows if r["probe"] == "no-signal"]
    if {r["variant"] for r in found} != CONTROLS or len(found) != len(CONTROLS):
        raise SystemExit("контрольные строки без сигнала: %s" % sorted(r["variant"] for r in found))
    return found


def check_rows(rows):
    for r in rows:
        if r["served"] + r["dropped"] != r["sent"]:
            raise SystemExit("%s: served + dropped != sent" % r["variant"])
        if r["probe"] == "sigterm" and r["in_flight_at_signal"] != r["sent"]:
            raise SystemExit("%s: сигнал пришёл, когда в полёте было %d из %d"
                             % (r["variant"], r["in_flight_at_signal"], r["sent"]))


def main():
    rows = read()
    check_rows(rows)
    for c in controls(rows):
        if c["dropped"] != 0:
            raise SystemExit("контроль %s: без сигнала оборвано %d" % (c["variant"], c["dropped"]))
    for r in rows:
        print(json.dumps({k: r[k] for k in ("variant", "served", "dropped", "accepted_after_signal",
                                             "exit_code", "exited_by_itself", "exit_after_signal_s")},
                         ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
