"""Ось 1: блокировка адресов источника — XDP, tc, iptables против счёта отправителя.

Отправитель шлёт по N UDP-пакетов с 10.99.0.1–4 (cmd/send), блокируются .2 и .3.
  none        — без фильтра (контроль);
  xdp-native  — XDP в режиме драйвера veth;
  xdp-generic — XDP в режиме skb;
  tc          — tc ingress (tcx);
  iptables    — правило INPUT DROP;
  xdp-update  — XDP: первая фаза без блокировки, затем адреса заносятся в карту
                на работающей программе.
Счёт: дошло до приложения по каждому адресу, увидел ли tcpdump на приёмнике,
счётчики программы.

    python3 scripts/run-filter.py              → все варианты, фикстура
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import common, stack  # noqa: E402
from scripts.outcome import END_MARKER, FILTER  # noqa: E402

N = 10000
BLOCK = ["10.99.0.2", "10.99.0.3"]
VARIANTS = ("none", "xdp-native", "xdp-generic", "tc", "iptables", "xdp-update")


def measure(name):
    r = stack.netsec(name, N, BLOCK)
    row = {"probe": "%d UDP per source, block %s" % (N, ",".join(BLOCK)), "mode": name}
    if "error" in r:
        row.update({"outcome": "error", "detail": "не поставить опыт: %s" % r["error"]})
        return row
    ph = r["phases"][-1]
    sent, got, seen = ph["sent"], ph["received"], ph["tcpdump"]
    blocked = [] if name == "none" else BLOCK
    want = {ip: (0 if ip in blocked else c) for ip, c in sent.items()}
    row.update({"sent": sent, "received": {ip: got.get(ip, 0) for ip in sent},
                "tcpdump_blocked": sum(seen.get(ip, 0) for ip in BLOCK),
                "prog_dropped": r.get("prog_dropped"), "prog_passed": r.get("prog_passed"),
                "iptables_dropped": r.get("iptables_dropped"), "socket_drops": ph["socket_drops"]})
    if name == "xdp-update":
        first = r["phases"][0]
        row["before_update_received"] = {ip: first["received"].get(ip, 0) for ip in first["sent"]}
    row["outcome"] = "match" if row["received"] == want else "mismatch"
    if ph["socket_drops"]:
        row["detail_socket"] = "очередь сокета отбросила %d" % ph["socket_drops"]
    dropped = sum(sent[ip] - row["received"][ip] for ip in sent)
    row["detail"] = "дошло %d из %d, от заблокированных %d; tcpdump видел заблокированных %d" % (
        sum(row["received"].values()), sum(sent.values()), sum(row["received"].get(ip, 0) for ip in BLOCK),
        row["tcpdump_blocked"])
    if r.get("prog_dropped") or r.get("prog_passed"):
        row["detail"] += "; программа: отброшено %d, пропущено %d" % (r["prog_dropped"], r["prog_passed"])
    if r.get("iptables_dropped") is not None:
        row["detail"] += "; iptables: отброшено %d" % r["iptables_dropped"]
    row["dropped_total"] = dropped
    return row


if __name__ == "__main__":
    common.run("filter", measure, VARIANTS, FILTER, END_MARKER)
