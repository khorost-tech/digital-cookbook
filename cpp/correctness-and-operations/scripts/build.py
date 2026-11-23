"""Сборка libparse и тестов в базовом образе: одна функция на все оси 9.x.

`build_script(planted, flags, compiler)` возвращает сценарий bash, который
копирует исходники из `/src` (только чтение) во временный каталог и собирает
CMake'ом. Ячейки матрицы оси 9.1 различаются только аргументами этой функции.
"""

SANITIZERS = {
    "plain": "",
    "asan": "-fsanitize=address -fno-omit-frame-pointer",
    "ubsan": "-fsanitize=undefined -fno-sanitize-recover=all",
    "tsan": "-fsanitize=thread",
}


def build_script(planted, flags="", compiler="g++", build_dir="/tmp/b"):
    return (
        "cmake -S /src -B {b} -DCMAKE_CXX_COMPILER={cc} -DPLANTED={p}"
        " -DCMAKE_CXX_FLAGS='-g {f}' >/dev/null"
        " && cmake --build {b} -j4 >/dev/null"
    ).format(b=build_dir, cc=compiler, p="ON" if planted else "OFF", f=flags)
