"""Ось 7: разбор. Контроли первыми.

analysistest прошёл — правило проверено и на отрицательных случаях. Обычный
бинарь не знает ctxloss — находки ctxloss принадлежат плагину, а не встроенному
линтеру. Ни один линтер не срабатывает в функциях без потери срока. Готовые
линтеры дают одно и то же в своём и обычном бинаре — плагин их не меняет.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import CUSTOM  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "custom.jsonl")


def read(path=FIXTURE):
    return CUSTOM.read_fixture(path)


def check(rows):
    by = {r["variant"]: r for r in rows}
    for r in rows:
        if r["outcome"] == "error":
            raise SystemExit("%s: %s" % (r["variant"], r["detail"]))
    if by["analysistest"]["rc"] != 0:
        raise SystemExit("analysistest не прошёл")
    if by["plain-binary"]["rc"] in (0, 1) or not by["plain-binary"]["error_text"]:
        raise SystemExit("контроль: обычный бинарь принял ctxloss")
    for v in ("custom", "builtin"):
        for linter, s in by[v]["linters"].items():
            if s["false"]:
                raise SystemExit("%s/%s: срабатывание в функции без потери срока %s" % (v, linter, s["false"]))
    for linter in by["builtin"]["linters"]:
        if by["builtin"]["linters"][linter] != by["custom"]["linters"][linter]:
            raise SystemExit("%s: в своём и обычном бинаре разный результат" % linter)


def main():
    rows = read()
    check(rows)
    for r in rows:
        print("%-14s %s" % (r["variant"], r["detail"]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
