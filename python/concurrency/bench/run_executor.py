"""Вынос счётной работы из цикла событий: в потоки или в процессы.

Совет «унеси счёт из цикла событий в run_in_executor» верен, но исполнитель
надо выбирать по сборке. На обычной сборке потоки счётную задачу не
ускоряют, и нужен пул процессов. На free-threaded сборке потоки работают.

Оба исполнителя меряются СИММЕТРИЧНО: одна и та же работа, одна и та же
обвязка asyncio, различается только класс пула. Без второго плеча
утверждение «потоки дешевле процессов» не следовало бы из этого замера —
сравнивать было бы не с чем.
"""

import asyncio
import json
import sys
import time
from concurrent.futures import ThreadPoolExecutor, ProcessPoolExecutor

from measure import gil_enabled
from workload import cpu_task

SIZE = 25000
COUNT = 400
WORKERS = (1, 2, 4, 8)


EXECUTORS = {"threads": ThreadPoolExecutor, "processes": ProcessPoolExecutor}


async def via_executor(kind, workers):
    loop = asyncio.get_running_loop()
    with EXECUTORS[kind](max_workers=workers) as ex:
        # Прогрев вне измеряемого интервала.
        await asyncio.gather(*[loop.run_in_executor(ex, cpu_task, SIZE)
                               for _ in range(workers)])
        t0 = time.perf_counter()
        got = await asyncio.gather(*[loop.run_in_executor(ex, cpu_task, SIZE)
                                     for _ in range(COUNT)])
        wall = time.perf_counter() - t0
    checksum = 0
    for r in got:
        checksum = (checksum * 31 + r) & 0xFFFFFFFF
    return COUNT / wall, checksum


async def main():
    build = sys.argv[1]
    rounds = int(sys.argv[2]) if len(sys.argv) > 2 else 3
    if build == "nogil" and gil_enabled():
        print("ОТКАЗ: запрошена сборка без GIL, но GIL включён", file=sys.stderr)
        return 2
    for r in range(1, rounds + 1):
        # Порядок числа воркеров сдвигается каждый круг.
        order = [WORKERS[(r + i) % len(WORKERS)] for i in range(len(WORKERS))]
        kinds = list(EXECUTORS)
        # Порядок исполнителей тоже сдвигается: иначе один из них всегда
        # получал бы более холодную машину.
        kinds = [kinds[(r + i) % len(kinds)] for i in range(len(kinds))]
        for w in order:
            for kind in kinds:
                rps, checksum = await via_executor(kind, w)
                print(json.dumps({"build": build, "gil_enabled": gil_enabled(),
                                  "python": sys.version.split()[0],
                                  "executor": kind, "workers": w,
                                  "count": COUNT, "round": r,
                                  "ops_per_sec": round(rps, 2),
                                  "checksum": checksum}, sort_keys=True))
    return 0


# Охрана обязательна: стартовый метод multiprocessing в Python 3.14 на
# Linux — forkserver, и служебный процесс переимпортирует главный модуль.
# Без охраны замер запускался бы повторно внутри него и рвал соединение.
if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
