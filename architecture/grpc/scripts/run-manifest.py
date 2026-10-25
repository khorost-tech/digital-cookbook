"""Сводка опубликованных величин прогона → fixtures/manifests/<сессия>.json.

Сводка — то, что попадает в статью: исходы и счёт, пересчитанные из фикстур.
Сессии сравниваются по ней, по общим вариантам. Не публикуется то, что зависит
от планирования, а не от предмета: сколько шагов исполнитель успел до отмены
(первый вызов JVM съедает часть срока), точное деление вызовов по бэкендам при
round_robin, номер попытки при сроке меньше паузы (пауза случайна). Поля с
суффиксом «~» — счёт вариантов, помеченных nondeterministic до прогона.

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
    p = {"protocol": {r["variant"]: r["observations"] for r in rows("protocol.jsonl")},
         "deadline": {r["variant"]: [r["outcome"], r["client_code"]] for r in rows("deadline.jsonl")},
         "cancel": {r["variant"]: [r["outcome"], r["stream_send_errors"] > 0]
                    + ([r["cancel_handler_before_end"] > 0] if r["mode"].endswith("-handler") else [])
                    for r in rows("cancel.jsonl")},
         "chain": {r["variant"]: ([r["metrics_calls"], r["metrics_by_code"], r["log_calls"], r["handler_calls"]]
                                  if r["kind"] == "chain" else [r["client_code"], r["metrics_by_code"]])
                   for r in rows("chain.jsonl")},
         "balance": {r["variant"]: r["outcome"] for r in rows("balance.jsonl")},
         "size": {r["variant"]: [r["outcome"], r["code"]] for r in rows("size.jsonl")}}
    er = {}
    for r in rows("errors.jsonl"):
        if r["kind"] == "errors":
            er[r["variant"]] = [r["code"], r["details_reason"], r["container_running"]]
        else:
            er[r["variant"]] = [r["http_status"], r["grpc_code"], r["body_reason"], r["lines"]]
    p["errors"] = er
    rt = {}
    for r in rows("retry.jsonl"):
        v = {"client_code": r["client_code"], "attempts": r["server_attempts"]}
        if r.get("nondeterministic"):
            v = {"client_code": r["client_code"], "attempts~": r["server_attempts"]}
        rt[r["variant"]] = v
    p["retry"] = rt
    p["keepalive"] = {r["variant"]: [r["outcome"], r.get("code", r.get("second"))] for r in rows("keepalive.jsonl")}
    return p


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
