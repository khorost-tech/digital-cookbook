"""Сводит картины двух профайлеров к сопоставимому виду.

cProfile даёт tottime — собственное время функции без вложенных вызовов.
py-spy даёт число сэмплов, в которых функция была ЛИСТОМ стека, то есть
исполнялась в момент снимка. Это и есть сопоставимые величины: обе отвечают
на вопрос "сколько времени процессор провёл внутри самой функции".
"""

import json
import pstats
import sys
from pathlib import Path

WATCH = ("tiny_step", "chunky_step")


def from_cprofile(path):
    st = pstats.Stats(str(path))
    shares, total = {}, 0.0
    for (fname, _line, func), entry in st.stats.items():
        tottime = entry[2]
        total += tottime
        if func in WATCH and Path(fname).name == "profile_target.py":
            shares[func] = shares.get(func, 0.0) + tottime
    calls = {}
    for (fname, _line, func), entry in st.stats.items():
        if func in WATCH and Path(fname).name == "profile_target.py":
            calls[func] = entry[0]
    return shares, total, calls


def from_pyspy_raw(path):
    """Свёрнутые стеки py-spy: 'кадр;кадр;кадр число'."""
    shares, total = {}, 0
    for line in Path(path).read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        stack, _, count = line.rpartition(" ")
        try:
            count = int(count)
        except ValueError:
            continue
        total += count
        leaf = stack.split(";")[-1]
        for w in WATCH:
            if leaf.startswith(w + " "):
                shares[w] = shares.get(w, 0) + count
    return shares, total


def main():
    """Печатает одну строку JSON: доли по версии каждого инструмента.

    Машиночитаемо намеренно — доли собираются в каждом круге, и выводы
    считает разбор, а не глаз по одной точке.
    """
    cprof_path, pyspy_path = sys.argv[1], sys.argv[2]
    c_shares, c_total, c_calls = from_cprofile(cprof_path)
    p_shares, p_total = from_pyspy_raw(pyspy_path)

    out = {"samples": p_total}
    for w in WATCH:
        out[w] = {
            "calls": c_calls.get(w, 0),
            "cprofile_pct": round(c_shares.get(w, 0.0) / c_total * 100, 2) if c_total else 0.0,
            "pyspy_pct": round(p_shares.get(w, 0) / p_total * 100, 2) if p_total else 0.0,
        }
    print(json.dumps(out, sort_keys=True))


if __name__ == "__main__":
    main()
