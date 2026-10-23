"""Сводка опубликованных величин прогона → fixtures/manifests/<сессия>.json.

Сводка — то, что попадает в статью: исходы и числа по осям, пересчитанные из
фикстур. Две сессии сравниваются по ней, а не по хешам файлов: адреса, время и
порядок прихода запросов меняются от прогона к прогону.

Что не сравнивается, названо явно:
  - поля с суффиксом «~» — счёт варианта, помеченного nondeterministic в
    фикстуре ДО сессий (опрос по таймеру, шторм реконнектов, распределение
    клиентов по рабочим процессам Angie); исход у них сравнивается, счёт — нет;
  - ось 6 (память на соединение) — величина, а не исход: сравнивается с
    допуском TOLERANCE (четверть величины), а не на равенство.

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
# RSS на соединение у Java гуляет между сессиями до пятой части (13,4–17,0 КБ на
# WebSocket в первых сессиях) — допуск шире, чем у Go, хватило бы и 0,05.
TOLERANCE = {"idle": 0.25}


def rows(name):
    with open(os.path.join(FX, name), encoding="utf-8") as f:
        return [json.loads(l) for l in f if l.startswith("{")]


def mark(row, values, counts):
    """Счёт недетерминированного варианта — под «~», вне сравнения."""
    if row.get("nondeterministic"):
        return {(k + "~" if k in counts else k): v for k, v in values.items()}
    return values


def published():
    p = {}
    p["protocol"] = {r["variant"]: r["observations"] for r in rows("protocol.jsonl")}
    p["flate"] = {str(r["level"]): r["bytes_per_message"] for r in rows("flate.jsonl")}
    p["wire"] = {r["variant"]: mark(r, {"outcome": r["outcome"], "down": r["down_per_event"],
                                        "up": r["up_per_event"], "requests": r["requests_per_event"],
                                        "empty": r["empty_per_event"]}, ("down", "up", "requests", "empty"))
                 for r in rows("wire.jsonl")}
    p["proxy"] = {r["variant"]: r["outcome"] for r in rows("proxy.jsonl")}
    # Ось 3: доля клиентов на инстансе и число дублей зависят от рабочих
    # процессов Angie — публикуется исход, инвариант проверяет analyze-scale.py.
    p["scale"] = {r["variant"]: r["outcome"] for r in rows("scale.jsonl")}
    rc = {}
    for r in rows("reconnect.jsonl"):
        if r["kind"] == "cancel":
            rc[r["variant"]] = {"outcome": r["outcome"], "conns_after": r["conns_after"]}
        elif r["kind"] == "storm":
            rc[r["variant"]] = mark(r, {"outcome": r["outcome"], "max": r["max_reconnects_per_100ms"]}, ("max",))
        else:
            rc[r["variant"]] = {"outcome": r["outcome"], "missing": r["missing"]}
    p["reconnect"] = rc
    # Ось 5: длина очереди и число выброшенных зависят от скорости чтения —
    # публикуется исход политики.
    p["slow"] = {r["variant"]: r["outcome"] for r in rows("slow.jsonl")}
    p["idle"] = {r["variant"]: {"rss": r["rss_kb_per_conn"], "heap": r["heap_kb_per_conn"]}
                 for r in rows("idle.jsonl")}
    return p


def strip(value):
    if isinstance(value, dict):
        return {k: strip(v) for k, v in value.items() if not k.endswith("~")}
    return value


def close(a, b, tol):
    if isinstance(a, dict) and isinstance(b, dict):
        return a.keys() == b.keys() and all(close(a[k], b[k], tol) for k in a)
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return abs(a - b) <= tol * max(abs(a), abs(b))
    return a == b


def compare(a, b, common_only=False):
    """(различия опубликованного, различались ли исключённые поля).

    common_only — сравнивать только варианты, которые есть в обеих сводках:
    вариант, добавленный в стенд между сессиями, — изменение состава, а не
    расхождение величин."""
    diffs, excluded_differ = [], False
    for axis in sorted(set(a) | set(b)):
        sa, sb = strip(a.get(axis)), strip(b.get(axis))
        tol = TOLERANCE.get(axis)
        keys = set(sa or {}) | set(sb or {})
        if common_only:
            keys = set(sa or {}) & set(sb or {})
        for key in sorted(keys):
            va, vb = (sa or {}).get(key), (sb or {}).get(key)
            same = close(va, vb, tol) if tol else va == vb
            if not same:
                diffs.append("%s/%s: %r → %r" % (axis, key, va, vb))
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
