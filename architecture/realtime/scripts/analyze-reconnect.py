"""Ось 4: разбор. Контроль первым.

Контроль разрыва: события до разрыва дошли, инстанс-подписчик сам сообщил
bus_state=down и до, и после публикации в разрыв, и снова up после
восстановления — иначе «потерь нет» могло бы значить «разрыва не было».
События до и после разрыва доходят у любой шины: потери — только из разрыва.
Шторм недетерминирован (помечен до прогона): проверяется исход, не счёт.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import CANCEL, RECONNECT, STORM  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "reconnect.jsonl")


def read(path=FIXTURE):
    rows = []
    text = open(path, encoding="utf-8").read()
    for line in text.splitlines():
        if line.startswith("{"):
            row = json.loads(line)
            {"storm": STORM, "cancel": CANCEL}.get(row["kind"], RECONNECT).parse_line(line)
            rows.append(row)
    if not rows or text.strip().splitlines()[-1] != RECONNECT.END_MARKER:
        raise SystemExit("%s: нет строк или маркера завершения" % path)
    return rows


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["kind"] == "gap":
            if not (r["bus_down_before_publish"] and r["bus_down_after_publish"] and r["bus_up_again"]):
                raise SystemExit("%s: разрыв не подтверждён инстансом" % r["variant"])
            if r["missing"] != r["missing_during_cut"]:
                raise SystemExit("%s: потеряно и вне разрыва — сломан опыт, а не шина" % r["variant"])
        if r["kind"] == "resume" and r.get("overlap"):
            if not r["barrier_seen"] or r["received_at_publish_end"] != r["n_before"]:
                raise SystemExit("%s: барьер не достигнут или история отдана до публикации — стыка не было"
                                 % r["variant"])
            late = r["variant"].endswith("-late")
            if late and r["outcome"] != "lost":
                raise SystemExit("контроль %s: подписка после истории не потеряла — опыт не способен провалиться"
                                 % r["variant"])
            if not late and r["outcome"] != "no-loss":
                raise SystemExit("%s: на стыке %s" % (r["variant"], r["detail"]))
        if r["kind"] == "resume" and r["outcome"] == "refused" and r["missing"] != r["n_during"] + r["n_after"]:
            raise SystemExit("%s: отказ, но события после разрыва дошли" % r["variant"])
        if r["kind"] == "storm" and not r.get("nondeterministic"):
            raise SystemExit("%s: шторм не помечен недетерминированным" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-28s %-8s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
