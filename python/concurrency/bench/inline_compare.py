"""Сколько на самом деле даёт устранение мелкой частой функции.

Детерминированный профайлер приписывает такой функции завышенную долю, потому
что платит фиксированную цену на каждом вызове. Отсюда типичный совет «убрать
вызов». Вопрос, который стенд проверяет: сколько это даёт БЕЗ профайлера.

Три варианта одной и той же работы:
* called  — как в profile_target: тело вынесено в функцию tiny_step;
* inlined — то же тело прямо в цикле, без вызова;
* chunky  — тот же объём работы одним вызовом на всю пачку.

Контрольная сумма у всех трёх обязана совпадать: иначе сравнивается не
скорость, а разный объём работы.
"""

import json
import sys
import time

from profile_target import tiny_step, TINY_PER_ITER

_MASK = 0xFFFFFFFF


def variant_called(iters):
    acc = 0
    for _ in range(iters):
        x = 1
        for _ in range(TINY_PER_ITER):
            x = tiny_step(x)
        acc = (acc * 31 + x) & _MASK
    return acc


def variant_inlined(iters):
    acc = 0
    for _ in range(iters):
        x = 1
        for _ in range(TINY_PER_ITER):
            x = (x * 1103515245 + 12345) & _MASK
        acc = (acc * 31 + x) & _MASK
    return acc


def variant_chunky(iters):
    # Тот же результат, но внутренний цикл спрятан в один вызов: так видно,
    # что экономится на вызовах, а что на самой работе.
    def run(n):
        x = 1
        for _ in range(n):
            x = (x * 1103515245 + 12345) & _MASK
        return x

    acc = 0
    for _ in range(iters):
        acc = (acc * 31 + run(TINY_PER_ITER)) & _MASK
    return acc


VARIANTS = ("called", "inlined", "chunky")


def main():
    iters = int(sys.argv[1]) if len(sys.argv) > 1 else 300
    rounds = int(sys.argv[2]) if len(sys.argv) > 2 else 7
    fns = {"called": variant_called, "inlined": variant_inlined,
           "chunky": variant_chunky}

    for f in fns.values():
        f(5)

    for r in range(1, rounds + 1):
        # Порядок вариантов сдвигается каждый круг: при фиксированном первый
        # систематически получает более холодную машину.
        order = [VARIANTS[(r + i) % len(VARIANTS)] for i in range(len(VARIANTS))]
        for name in order:
            t0 = time.perf_counter()
            checksum = fns[name](iters)
            print(json.dumps({"round": r, "variant": name, "iters": iters,
                              "elapsed_s": round(time.perf_counter() - t0, 4),
                              "checksum": checksum}, sort_keys=True))


if __name__ == "__main__":
    main()
