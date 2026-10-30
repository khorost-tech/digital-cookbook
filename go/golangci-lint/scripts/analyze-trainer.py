"""Тренажёр: разбор.

Нетронутый уровень обязан быть красным — иначе читатель увидит PASS, ничего не
сделав. Эталон обязан быть зелёным — иначе уровень нельзя пройти. У каждого
замечания старта есть подсказка — грейдер не выдаёт сырой лог без объяснения.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import TRAINER  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "trainer.jsonl")


def read(path=FIXTURE):
    return TRAINER.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["state"] == "start" and r["outcome"] != "red":
            raise SystemExit("%s: нетронутый уровень зелёный" % r["variant"])
        if r["state"] == "solution" and r["outcome"] != "green":
            raise SystemExit("%s: эталон не проходит" % r["variant"])
        if r["no_hint"]:
            raise SystemExit("%s: замечания без подсказки %s" % (r["variant"], r["no_hint"]))
    if {r["level"] for r in rows} != {"l1", "l2", "l3", "l4"}:
        raise SystemExit("проверены не все уровни")


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-12s %-6s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
