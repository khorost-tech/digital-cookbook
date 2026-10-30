"""Оси 1–2: разбор. Контроль первым.

Контроль: исправленные пары под курируемым конфигом — ноль находок; иначе
линтер срабатывает не на дефект, и счёт по разметке ничего не значит.
Курируемый конфиг обязан найти всё размеченное без шума и лишнего — иначе
сравнение с ним не отделяет «не включён» от «не ловит».
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import DEFAULT  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "default.jsonl")


def read(path=FIXTURE):
    return DEFAULT.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if by["fixed-curated"]["issues"]:
        raise SystemExit("контроль: на исправленных парах есть находки")
    c = by["curated"]
    if c["missed"] or c["noise"] or c["extra"]:
        raise SystemExit("курируемый конфиг: пропущено %d, шум %d, лишних %d" % (c["missed"], c["noise"], c["extra"]))
    for r in rows:
        if "--max-same-issues 0" not in r["lint_cmd"] or "--uniq-by-line=false" not in r["lint_cmd"]:
            raise SystemExit("%s: счёт с обрезкой вывода" % r["variant"])


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-14s %-7s %s" % (r["variant"], r["outcome"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
