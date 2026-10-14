"""Сколько потоков живёт в РОДИТЕЛЬСКОМ процессе при workers=1.

Проверяет посылку "при одном воркере параллелизма нет ни у кого". Если у
модели в родителе больше одного потока, то на сборке с GIL она платит за
передачу GIL между потоками даже при одном воркере — и тогда сравнение
сборок в этом плече измеряет не только накладные расходы модели памяти.
"""
import asyncio
import multiprocessing
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, ProcessPoolExecutor

from workload import cpu_task


def peak_threads(make):
    peak = 0
    stop = threading.Event()

    def watch():
        nonlocal peak
        while not stop.is_set():
            peak = max(peak, threading.active_count())
            time.sleep(0.002)

    w = threading.Thread(target=watch)
    w.start()
    try:
        make()
    finally:
        # Без finally исключение в make() оставило бы наблюдателя крутиться
        # вечно, а процесс — жить и потреблять CPU. Такой беглец потом
        # искажает следующий замер, и найти его непросто.
        stop.set()
        w.join()
    return peak - 1          # сам наблюдатель не в счёт


def m_asyncio():
    async def go():
        for _ in range(20):
            cpu_task(25000)
    asyncio.run(go())


def m_threads():
    with ThreadPoolExecutor(max_workers=1) as ex:
        list(ex.map(cpu_task, [25000] * 20))


def m_processes():
    with ProcessPoolExecutor(max_workers=1) as ex:
        list(ex.map(cpu_task, [25000] * 20))


if __name__ == "__main__":
    print(f"Python {sys.version.split()[0]}, GIL={sys._is_gil_enabled()}")
    print(f"стартовый метод multiprocessing: {multiprocessing.get_start_method()}")
    print("пиковое число потоков в родительском процессе при workers=1:")
    for name, fn in (("asyncio", m_asyncio),
                     ("threads", m_threads),
                     ("processes", m_processes)):
        print(f"  {name:<11} {peak_threads(fn)}")
