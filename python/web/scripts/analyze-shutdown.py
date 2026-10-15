"""Разбор: действительно ли завершение корректное.

Главная проверка здесь не «мало ли потерь», а «умеет ли сценарий их вообще
замечать». Обрыв по SIGKILL обязан дать потери: если их нет и там, значит
замер слеп, и «ноль потерь при SIGTERM» ничего не доказывает.

Коды возврата:

* 0 — замер годен;
* 2 — фикстура пуста или неполна;
* 3 — контроль не сработал: SIGKILL не дал потерь, и сценарий их не ловит.
"""

import json
import os
import sys
from pathlib import Path

FIX = Path(os.environ.get("FIXTURES_DIR",
                          Path(__file__).resolve().parent.parent / "fixtures"))


def rows():
    path = FIX / "shutdown.txt"
    return [json.loads(l) for l in path.read_text(encoding="utf-8").splitlines()
            if l.startswith("{")]


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def main():
    data = rows()
    if not data:
        print("ОТКАЗ: фикстура пуста", file=sys.stderr)
        return 2

    servers = sorted({r["server"] for r in data})

    print("=" * 74)
    print("КОНТРОЛЬ: умеет ли сценарий замечать потери")
    print("=" * 74)
    print()
    blind = []
    for s in servers:
        killed = [r for r in data if r["server"] == s and r["how"] == "sigkill"]
        if not killed:
            print(f"  {s:<10} контрольных прогонов нет")
            blind.append(s)
            continue
        lost = med([r["inflight_lost"] for r in killed])
        infl = med([r["inflight"] for r in killed])
        print(f"  {s:<10} при SIGKILL потеряно из бывших в полёте: "
              f"{lost:.0f} из {infl:.0f}")
        if lost == 0:
            blind.append(s)

    print()
    if blind:
        print("ВЕРДИКТ: КОНТРОЛЬ НЕ СРАБОТАЛ — " + ", ".join(blind))
        print("  Обрыв без дренажа не дал потерь, значит сценарий их не ловит.")
        print("  «Ноль потерь при штатном завершении» здесь ничего не доказывает.")
        return 3
    print("ВЕРДИКТ: контроль сработал — потери при обрыве видны")

    print()
    print("=" * 74)
    print("ШТАТНОЕ ЗАВЕРШЕНИЕ ПРОТИВ ОБРЫВА")
    print("=" * 74)
    print()
    print(f"  {'сервер':<10}{'способ':<10}{'в полёте':>11}{'потеряно':>11}"
          f"{'доля':>8}{'после сигнала':>15}{'время, с':>10}")
    for s in servers:
        for how in ("sigterm", "sigkill"):
            sel = [r for r in data if r["server"] == s and r["how"] == how]
            if not sel:
                continue
            infl = med([r["inflight"] for r in sel])
            lost = med([r["inflight_lost"] for r in sel])
            after = med([r["started_after_signal"] for r in sel])
            secs = med([r["shutdown_s"] for r in sel])
            share = lost / infl * 100 if infl else 0
            print(f"  {s:<10}{how:<10}{infl:>11.0f}{lost:>11.0f}"
                  f"{share:>7.1f}%{after:>15.0f}{secs:>10.2f}")

    print()
    print("=" * 74)
    print("ЧТО ИЗ ЭТОГО СЛЕДУЕТ")
    print("=" * 74)
    print()
    for s in servers:
        t = [r for r in data if r["server"] == s and r["how"] == "sigterm"]
        k = [r for r in data if r["server"] == s and r["how"] == "sigkill"]
        if not (t and k):
            continue
        lt = med([r["inflight_lost"] for r in t])
        lk = med([r["inflight_lost"] for r in k])
        if lt == 0:
            print(f"  {s}: штатное завершение не потеряло ни одного запроса, "
                  f"тогда как обрыв потерял {lk:.0f}")
        else:
            print(f"  {s}: штатное завершение всё равно потеряло {lt:.0f} "
                  f"против {lk:.0f} при обрыве — дренаж неполон")
    return 0


if __name__ == "__main__":
    sys.exit(main())
