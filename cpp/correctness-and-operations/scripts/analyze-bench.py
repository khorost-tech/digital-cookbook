"""Ось 2: осталась ли измеряемая работа в собранной функции бенчмарка.

Работа — 1000 итераций с умножением внутри цикла итераций бенчмарка. Её
признак в дизассемблере тела (arm64):
  - умножение: mul, madd, umull, umaddl, smull, mla (и векторные формы);
  - вложенный цикл, содержащий умножение: обратный переход, чей диапазон
    [цель, переход] содержит умножение и сам лежит внутри диапазона другого
    обратного перехода (цикл итераций бенчмарка).
Исходы: kept — умножение во вложенном цикле; replaced — умножения есть, но
вложенного цикла с ними нет (компилятор заменил цикл формулой); lost —
умножений нет; error — тела нет по символу (встроено или не собрано): «работы
нет» тогда было бы ложью.

Бенчмарк не запускается: время в этой оси не меряется вовсе (спека 30.09).
"""

import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from scripts.outcome import BENCH  # noqa: E402

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(HERE, "fixtures", "bench.jsonl")
MUL = re.compile(r"\s(mul|madd|umull|umaddl|smull|smaddl|mla)\s")
BRANCH = re.compile(r"^\s*([0-9a-f]+):\s+(b\.\w+|b|cbn?z|tbn?z)\s+(?:[wx]\d+,\s*)?(?:#\S+,\s*)?([0-9a-f]+)\s")
INSN = re.compile(r"^\s*([0-9a-f]+):\s")


def body_of(disassembly, symbol):
    out, keep = [], False
    for line in disassembly.splitlines():
        m = re.match(r"^[0-9a-f]+ <(.+)>:$", line)
        if m:
            keep = m.group(1).startswith(symbol + "(")
            continue
        if keep and line.strip():
            out.append(line)
    return out


def judge(disassembly, symbol):
    body = body_of(disassembly, symbol)
    if not body:
        return {"outcome": "error", "detail": "символ %s не найден" % symbol,
                "symbol_found": False, "work_present": False, "instructions": 0}
    muls = [int(INSN.match(l).group(1), 16) for l in body if MUL.search(l) and INSN.match(l)]
    loops = []
    for l in body:
        m = BRANCH.match(l)
        if m:
            at, target = int(m.group(1), 16), int(m.group(3), 16)
            if target < at:
                loops.append((target, at))
    nested = [lw for lw in loops
              if any(lw[0] <= x <= lw[1] for x in muls)
              and any(ls != lw and ls[0] <= lw[0] and lw[1] <= ls[1] for ls in loops)]
    n = sum(1 for l in body if INSN.match(l))
    if nested:
        outcome, detail = "kept", "умножение во вложенном цикле"
    elif muls:
        outcome, detail = "replaced", "умножения есть (%d), вложенного цикла с ними нет" % len(muls)
    else:
        outcome, detail = "lost", "умножений нет"
    return {"outcome": outcome, "detail": detail, "symbol_found": True,
            "work_present": outcome == "kept", "multiplies": len(muls),
            "loops": len(loops), "instructions": n}


def read(path=FIXTURE):
    return BENCH.read_fixture(path)


def rows_for(rows, **match):
    return [r for r in rows if all(r.get(k) == v for k, v in match.items())]


def check_controls(rows):
    bad = [r for r in rows_for(rows, opt="O0") if r["writing"] != "empty" and not r["work_present"]]
    if bad:
        raise SystemExit("-O0 потерял работу: %s" % bad[0]["probe"])
    noisy = [r for r in rows_for(rows, writing="empty") if r["work_present"] or r["outcome"] != "lost"]
    if noisy:
        raise SystemExit("пустая функция не пуста: %s" % noisy[0]["probe"])
    errors = rows_for(rows, outcome="error")
    if errors:
        raise SystemExit("символ не найден: %s" % errors[0]["probe"])


def main():
    rows = read()
    check_controls(rows)
    writings = ["naive", "do_not_optimize", "clobber", "opaque_input", "empty"]
    cols = sorted({(r["compiler"], r["opt"]) for r in rows})
    print("%-16s" % "" + " ".join("%-12s" % ("%s %s" % c) for c in cols))
    for w in writings:
        cells = {(r["compiler"], r["opt"]): r["outcome"] for r in rows_for(rows, writing=w)}
        print("%-16s" % w + " ".join("%-12s" % cells.get(c, "—") for c in cols))
    return 0


if __name__ == "__main__":
    sys.exit(main())
