"""Общее раннеров: запись фикстуры, разбор аргументов, строка с исходом."""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402
from scripts.outcome import END_MARKER, MATCH  # noqa: E402

SHOW = ("variant", "outcome", "detail")

with open(os.path.join(stack.HERE, "expected.json"), encoding="utf-8") as f:
    EXPECTED = json.load(f)


def fixture(axis):
    return os.path.join(stack.HERE, "fixtures", "%s.jsonl" % axis)


def read(axis):
    return {r["variant"]: r for r in MATCH.read_fixture(fixture(axis))}


def run(axis, measure, variants):
    """Все варианты — фикстура; аргументы — только эти варианты, строки JSON.

    measure(имя) возвращает строку или список строк (одна команда bench может
    дать и контроль, и вариант).
    """
    stack.up()
    names = sys.argv[1:] or list(variants)
    rows = []
    for n in names:
        got = measure(n)
        for m in got if isinstance(got, list) else [got]:
            variant = m.pop("variant", n)
            r = {"kind": axis, "variant": variant, "probe": m.pop("probe"),
                 "cmd": "python3 scripts/run-%s.py %s" % (axis, n), **m}
            MATCH.parse_line(json.dumps(r, ensure_ascii=False))
            rows.append(r)
            print(json.dumps({k: r[k] for k in SHOW}, ensure_ascii=False), flush=True)
    if not sys.argv[1:]:
        with open(fixture(axis), "w", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
            f.write(END_MARKER + "\n")


def outcome(ok):
    return "match" if ok else "mismatch"


def monotonic(values, slack=0.002):
    """Не убывает с точностью до slack (параллельное построение даёт шум)."""
    return all(b >= a - slack for a, b in zip(values, values[1:]))


def tilde(points, keys):
    """Шумные поля точек — с «~»: в сравнение сессий не идут."""
    return [{(k + "~" if k in keys else k): v for k, v in p.items()} for p in points]
