"""Общее раннеров: запись фикстуры, разбор аргументов, порядок полей строки."""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts import stack  # noqa: E402

SHOW = ("variant", "outcome", "detail")


def run(axis, measure, variants, contract, end_marker):
    """Все варианты — фикстура; аргументы — только эти варианты, строки JSON."""
    stack.up()
    names = sys.argv[1:] or list(variants)
    rows = []
    for n in names:
        m = measure(n)
        r = {"kind": axis, "variant": n, "probe": m.pop("probe"), "cmd": "python3 scripts/run-%s.py %s" % (axis, n),
             **m}
        contract.parse_line(json.dumps(r, ensure_ascii=False))
        rows.append(r)
        print(json.dumps({k: r[k] for k in SHOW}, ensure_ascii=False), flush=True)
    if not sys.argv[1:]:
        with open(os.path.join(stack.HERE, "fixtures", "%s.jsonl" % axis), "w", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")
            f.write(end_marker + "\n")
