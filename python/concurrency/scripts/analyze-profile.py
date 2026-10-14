"""Разбор фикстуры профилирования.

Отвечает на два вопроса. Первый: сколько стоит сам инструмент. Второй:
насколько расходятся картины детерминированного и сэмплирующего
профайлеров на одной и той же нагрузке.

Второй вопрос требует осторожности. Эталона здесь нет: измерить "истинную"
долю функции нельзя, не воспользовавшись каким-нибудь профайлером. Поэтому
корректная формулировка — не "cProfile ошибается на N процентов", а
"инструменты расходятся во столько-то раз", и обе картины верны каждая в
своих терминах.
"""

import json
import os
import sys
from pathlib import Path

FIX = Path(os.environ.get("FIXTURES_DIR",
                          Path(__file__).resolve().parent.parent / "fixtures"))
WATCH = ("tiny_step", "chunky_step")


def med(xs):
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 else (s[n // 2 - 1] + s[n // 2]) / 2


def main():
    times, shares = {}, []
    for line in (FIX / "profile.txt").read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line.startswith("#") or not line:
            continue
        if "режим=" in line:
            mode = line.split("режим=")[1].split()[0]
            val = float(line.split("elapsed_s=")[1])
            times.setdefault(mode, []).append(val)
        elif "доли=" in line:
            shares.append(json.loads(line.split("доли=", 1)[1]))

    if not times or not shares:
        print("ОТКАЗ: фикстура пуста или не разобрана", file=sys.stderr)
        return 2

    print("=" * 70)
    print("ЦЕНА ИНСТРУМЕНТА: время одной и той же нагрузки")
    print("=" * 70)
    print()
    base = med(times["без_профайлера"])
    for mode in ("без_профайлера", "cProfile", "py-spy"):
        v = times[mode]
        print(f"  {mode:<16} медиана {med(v):6.3f} c  [{min(v):.3f}; {max(v):.3f}]"
              f"  = {med(v) / base:.2f}x к прогону без профайлера")
    print()
    over = (med(times["cProfile"]) / base - 1) * 100
    light = (med(times["py-spy"]) / base - 1) * 100
    print(f"  cProfile добавляет {over:+.0f}% времени, py-spy {light:+.0f}%.")
    print("  Разница не в качестве инструментов, а в устройстве: один")
    print("  перехватывает каждый вызов, другой смотрит на стек по таймеру.")

    print()
    print("=" * 70)
    print("РАСХОЖДЕНИЕ КАРТИН на одной и той же нагрузке")
    print("=" * 70)
    print()
    print(f"  кругов: {len(shares)}, сэмплов у py-spy за круг: "
          f"{med([s['samples'] for s in shares]):.0f}")
    print()
    print(f"  {'функция':<14}{'вызовов':>10}{'cProfile':>11}{'py-spy':>11}{'отношение':>12}{'диапазон':>14}")
    same_dir = {}
    for w in WATCH:
        calls = shares[0][w]["calls"]
        c = [s[w]["cprofile_pct"] for s in shares]
        p = [s[w]["pyspy_pct"] for s in shares]
        ratios = [ci / pi for ci, pi in zip(c, p) if pi]
        up = sum(1 for x in ratios if x > 1)
        same_dir[w] = (up, len(ratios))
        print(f"  {w:<14}{calls:>10}{med(c):>10.1f}%{med(p):>10.1f}%"
              f"{med(ratios):>11.2f}x{min(ratios):>8.2f}..{max(ratios):.2f}")
    print()
    print("  Устойчиво здесь НЕ значение множителя, а направление. Разброс")
    print("  идёт почти целиком от шума сэмплирования: у cProfile доли из")
    print("  круга в круг почти не меняются, а py-spy на меньшей доле имеет")
    print("  пропорционально меньше сэмплов и потому шумит сильнее.")
    print()
    inconsistent = []
    for w, (up, n) in same_dir.items():
        if up == n or up == 0:
            side = "выше" if up == n else "ниже"
            print(f"  {w}: доля по cProfile {side} во ВСЕХ {n} кругах из {n}")
        else:
            print(f"  {w}: направление НЕ постоянно ({up} из {n}) — вывода нет")
            inconsistent.append(f"{w}: {up} из {n}")

    # Статья утверждает постоянство направления. Если оно не постоянно,
    # утверждение остаётся без основания, и разбор обязан это назвать
    # ненулевым кодом, а не только строчкой в выводе.
    if inconsistent:
        print()
        print("ВЕРДИКТ: НЕГОДЕН — направление расхождения непостоянно: "
              + ", ".join(inconsistent))
        return 4
    print()
    print("  Читать это надо так: cProfile приписывает мелкой частой функции")
    print("  большую долю, чем сэмплирующий профайлер, потому что платит")
    print("  фиксированную цену на каждом её вызове. Эталонной доли здесь нет:")
    print("  измерить её можно только ещё каким-нибудь профайлером.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
