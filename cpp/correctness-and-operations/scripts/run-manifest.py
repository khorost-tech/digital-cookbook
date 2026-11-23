"""Сводка опубликованных величин прогона → fixtures/manifests/<сессия>.json.

Сводка — то, что попадает в статьи: исходы и числа по осям, пересчитанные из
фикстур. Две сессии сравниваются по ним, а не по хешам файлов: вывод
инструмента (адреса, pid, время) меняется от прогона к прогону и сравнения
не заслуживает. Исключённые поля перечислены в EXCLUDED и обязаны РАЗЛИЧАТЬСЯ
хотя бы в одном месте между сессиями: совпали — исключение либо не работает,
либо бессмысленно.

    python3 scripts/run-manifest.py                 → сводка текущих фикстур
    python3 scripts/run-manifest.py compare A B     → сравнить две сводки
"""

import datetime
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import BENCH, CONFIG, DEFECTS, SERVED, read_measurements  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FX = os.path.join(HERE, "fixtures")
MANIFESTS = os.path.join(FX, "manifests")

# Поля, которые в сводке есть, но в сравнении не участвуют.
EXCLUDED = {
    "shutdown": ["exit_after_signal_s",           # время до выхода — не величина оси
                 # счёт варианта, недетерминированность которого известна до
                 # прогона (nondeterministic в фикстуре): он и обязан различаться
                 "served~", "dropped~", "truncated~", "outcome~"],
    "defects": ["outcome~", "caught_in~"],    # клетка, недетерминированность которой предсказана
    "config": ["effective_port@empty", "effective_port@letters"],   # случайный порт ядра
}


def published():
    p = {"defects": {}, "bench": {}, "shutdown": {}, "config": {}, "fds": {}, "logs": {}, "coverage": {}}
    for r in DEFECTS.read_fixture(os.path.join(FX, "defects.jsonl")):
        key = "%s/%s-%s" % (r["program"], r["detector"], r["opt"])
        if r.get("nondeterministic"):
            p["defects"][key] = {"outcome~": r["outcome"], "caught_in~": r["caught_in"],
                                 "repeats": r["repeats"], "present": r["defect_present"]}
        else:
            p["defects"][key] = [r["outcome"], r["defect_present"]]
    for r in BENCH.read_fixture(os.path.join(FX, "bench.jsonl")):
        p["bench"]["%s/%s-%s" % (r["writing"], r["compiler"], r["opt"])] = r["outcome"]
    for r in SERVED.read_fixture(os.path.join(FX, "shutdown.jsonl")):
        v = {k: r[k] for k in (
            "outcome", "served", "dropped", "truncated", "accepted_after_signal", "exit_code",
            "exited_by_itself", "exit_after_signal_s")}
        v["repeats"] = r.get("repeats", 1)
        if r.get("nondeterministic"):
            v = {(k + "~" if k in ("served", "dropped", "truncated", "outcome") else k): x for k, x in v.items()}
        p["shutdown"][r["variant"]] = v
    for r in CONFIG.read_fixture(os.path.join(FX, "config.jsonl")):
        p["config"]["%s/%s" % (r["reader"], r["input_name"])] = {
            "outcome": r["outcome"], "effective_port@%s" % r["input_name"]: r["effective_port"]}
    for r in read_measurements(os.path.join(FX, "fds.jsonl")):
        p["fds"][r["variant"]] = r["delta"]
    for r in read_measurements(os.path.join(FX, "logs.jsonl")):
        p["logs"][r["variant"]] = [r["lines"], r["parsed"], r["valid_utf8"], r["round_trip"]]
    for r in read_measurements(os.path.join(FX, "coverage.jsonl")):
        if r["kind"] == "coverage":
            p["coverage"]["summary"] = r["summary"]
            p["coverage"]["uncovered"] = r["uncovered"]
            p["coverage"]["suite_passed"] = r["suite_passed"]
        else:
            p["coverage"]["boundary/" + r["planted"]] = r["boundary_passed"]
    return p


def strip(axis, value):
    """Убрать исключённые поля оси из значения (словарь или вложенный словарь)."""
    if isinstance(value, dict):
        return {k: strip(axis, v) for k, v in value.items() if k not in EXCLUDED.get(axis, [])}
    return value


def compare(a, b):
    """(различия опубликованного, различались ли исключённые поля)."""
    diffs, excluded_differ = [], False
    for axis in a:
        sa, sb = strip(axis, a[axis]), strip(axis, b[axis])
        if sa != sb:
            for key in sorted(set(sa) | set(sb)):
                if sa.get(key) != sb.get(key):
                    diffs.append("%s/%s: %r → %r" % (axis, key, sa.get(key), sb.get(key)))
        if a[axis] != b[axis] and sa == sb:
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
        return 1 if diffs or not excluded else 0
    os.makedirs(MANIFESTS, exist_ok=True)
    session = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    path = os.path.join(MANIFESTS, "%s.json" % session)
    with open(path, "w", encoding="utf-8") as f:
        json.dump({"session": session, "published": published()}, f, ensure_ascii=False, indent=1)
    print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
