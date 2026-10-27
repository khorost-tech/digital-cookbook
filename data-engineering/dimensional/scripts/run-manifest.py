"""Сводка опубликованных величин прогона → fixtures/manifests/<сессия>.json.

Сводка — то, что попадает в статью: исходы и счёт против эталона, пересчитанные
из фикстур. Сессии сравниваются по ней, по общим вариантам. Даты запусков и
идентификаторы в сводку не входят.

    python3 scripts/run-manifest.py                 → сводка текущих фикстур
    python3 scripts/run-manifest.py compare A B     → сравнить две сводки
"""

import datetime
import json
import os
import sys

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FX = os.path.join(HERE, "fixtures")
MANIFESTS = os.path.join(FX, "manifests")


def rows(name):
    with open(os.path.join(FX, name), encoding="utf-8") as f:
        return [json.loads(l) for l in f if l.startswith("{")]


def published():
    keep = ("outcome", "misattributed", "wrong_orders", "wrong_amount", "wrong_once", "wrong_twice", "got_total", "ratio", "got", "missing",
            "late_orders_kept", "late_regions", "got_orders")
    return {axis: {r["variant"]: {k: r[k] for k in keep if k in r} for r in rows(axis + ".jsonl")}
            for axis in ("scd", "grain", "additivity", "keys")}


def strip(value):
    if isinstance(value, dict):
        return {k: strip(v) for k, v in value.items() if not k.endswith("~")}
    return value


def compare(a, b, common_only=False):
    """(различия опубликованного, различались ли исключённые поля)."""
    diffs, excluded_differ = [], False
    for axis in sorted(set(a) | set(b)):
        sa, sb = strip(a.get(axis)) or {}, strip(b.get(axis)) or {}
        keys = (set(sa) & set(sb)) if common_only else (set(sa) | set(sb))
        for key in sorted(keys):
            if sa.get(key) != sb.get(key):
                diffs.append("%s/%s: %r → %r" % (axis, key, sa.get(key), sb.get(key)))
        if a.get(axis) != b.get(axis) and sa == sb:
            excluded_differ = True
    return diffs, excluded_differ


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "compare":
        with open(sys.argv[2]) as f:
            a = json.load(f)["published"]
        with open(sys.argv[3]) as f:
            b = json.load(f)["published"]
        diffs, excluded = compare(a, b)
        for d in diffs:
            print("различие:", d)
        print("исключённые поля различаются:", excluded)
        return 1 if diffs else 0
    if len(sys.argv) != 1:
        raise SystemExit(__doc__)
    os.makedirs(MANIFESTS, exist_ok=True)
    session = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    path = os.path.join(MANIFESTS, "%s.json" % session)
    with open(path, "w", encoding="utf-8") as f:
        json.dump({"session": session, "published": published()}, f, ensure_ascii=False, indent=1)
    print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
