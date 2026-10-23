"""Ось 3: разбор. Контроль первым.

Контроль: клиенты разошлись по обоим инстансам (иначе «без шины дошло всем» —
ложь), с шиной все клиенты получили все события. Сколько клиентов попадёт на
каждый инстанс, зависит от рабочих процессов Angie (у каждого свой счётчик
очереди), поэтому проверяется не доля, а инвариант: без шины все события
получили ровно клиенты инстанса, в который публиковали, а клиенты другого — ни
одного. Число дублей long polling тоже зависит от того, как легли запросы, —
проверяется исход, а не счёт.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import SCALE  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "scale.jsonl")
A, B = "rt-sc-a", "rt-sc-b"


def read(path=FIXTURE):
    return SCALE.read_fixture(path)


def check(rows):
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
        if r["kind"] == "fanout":
            per = r["clients_per_instance"]
            if set(per) != {A, B} or min(per.values()) == 0:
                raise SystemExit("%s: клиенты не разошлись по инстансам: %s" % (r["variant"], per))
            if r["bus"] != "none" and r["outcome"] != "all":
                raise SystemExit("контроль %s: с шиной дошло не всем" % r["variant"])
            if r["bus"] == "none":
                if r["full_per_instance"] != {A: per[A]} or r["clients_none"] != per[B]:
                    raise SystemExit("%s: без шины получили не ровно клиенты %s" % (r["variant"], A))
        if r["kind"] == "longpoll" and r["angie_conf"] == "sticky" and r["outcome"] != "all":
            raise SystemExit("контроль %s: со sticky есть пропуски или дубли" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-24s %-8s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
