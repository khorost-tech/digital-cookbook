"""Запуск команды на заданном наборе ядер.

Зачем. Нагрузчик и цель живут на одной машине и по умолчанию делят все ядра.
При двенадцати процессах нагрузчика цель получает примерно одну тринадцатую
процессора, и замер превращается в соревнование за ядра: пропускная
способность цели падает тем сильнее, чем сильнее её обстреливают.

Это видно по развёртке: без разведения по ядрам суммарная пропускная
способность росла с числом процессов нагрузчика и не выходила на полку —
не потому, что цель не насыщена, а потому, что у неё отбирали процессор.

Разведение по непересекающимся наборам ядер убирает эту связь: цель всегда
имеет свои ядра, сколько бы процессов ни было у нагрузчика.

Использование:
    python pin.py 0-3 -- uvicorn app_raw:asgi_app --port 8004
"""

import os
import sys


def parse_cpus(spec):
    """Разбирает '0-3' и '0,2,5-7' в множество номеров ядер."""
    cpus = set()
    for part in spec.split(","):
        part = part.strip()
        if not part:
            continue
        if "-" in part:
            lo, hi = part.split("-", 1)
            cpus.update(range(int(lo), int(hi) + 1))
        else:
            cpus.add(int(part))
    return cpus


def main():
    if "--" not in sys.argv:
        print("использование: pin.py <ядра> -- <команда>", file=sys.stderr)
        return 2
    sep = sys.argv.index("--")
    cpus = parse_cpus(sys.argv[1])
    command = sys.argv[sep + 1:]
    if not command:
        print("нечего запускать", file=sys.stderr)
        return 2

    available = os.sched_getaffinity(0)
    unknown = cpus - available
    if unknown:
        print(f"ОТКАЗ: ядер {sorted(unknown)} нет в доступном наборе "
              f"{sorted(available)}", file=sys.stderr)
        return 2

    os.sched_setaffinity(0, cpus)
    os.execvp(command[0], command)


if __name__ == "__main__":
    sys.exit(main())
