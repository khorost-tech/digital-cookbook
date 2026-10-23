"""Ось 6: разбор. Контроль первым.

Контроль: открыты все K соединений, и два базовых замера расходятся меньше,
чем на десятую долю прироста от K соединений, — иначе «память на соединение»
была бы шумом базы.
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import END_MARKER  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "idle.jsonl")


def read(path=FIXTURE):
    text = open(path, encoding="utf-8").read()
    rows = [json.loads(l) for l in text.splitlines() if l.startswith("{")]
    if not rows or text.strip().splitlines()[-1] != END_MARKER:
        raise SystemExit("%s: нет строк или маркера завершения" % path)
    for r in rows:
        if not r.get("cmd"):
            raise SystemExit("%s: нет команды" % r.get("variant"))
    return rows


def check(rows):
    for r in rows:
        if r["opened"] != r["k"]:
            raise SystemExit("%s: открыто %d из %d" % (r["variant"], r["opened"], r["k"]))
        grew = r["loaded"]["rss_kb"] - r["base2"]["rss_kb"]
        if r["base_drift_kb"] * 10 > grew:
            raise SystemExit("%s: база гуляет на %d КБ при приросте %d КБ" % (r["variant"], r["base_drift_kb"], grew))


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-9s RSS %5.1f КБ/соед., куча %5.1f КБ/соед." % (r["variant"], r["rss_kb_per_conn"],
                                                                r["heap_kb_per_conn"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
