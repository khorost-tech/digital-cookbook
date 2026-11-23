"""Сборка базового образа и запуск команд в нём.

Единственное место, где собирается `docker run`: пробы и оси зовут `run_in_base`,
а не строят команду сами — иначе один замер шёл бы с `--privileged`, а другой без,
и различие величин было бы различием обвязки.

Исходники стенда монтируются только для чтения в `/src`; сборка идёт во временный
каталог контейнера. `cmd`, который попадает в фикстуру, — это команда ВНУТРИ
контейнера плюс строка запуска контейнера, чтобы читатель повторил шаг дословно.
"""

import os
import shlex
import subprocess

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE_TAG = "cpp-correctness-and-operations-base:latest"


def build_base(no_cache=False):
    cmd = ["docker", "build", "-q", "-t", BASE_TAG, "-f",
           os.path.join(HERE, "images", "Dockerfile.base")]
    if no_cache:
        cmd.append("--no-cache")
    cmd.append(HERE)
    subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL)


def base_command(script, privileged=False):
    """Строка `docker run …` для показа читателю: запускать из каталога стенда."""
    parts = ["docker", "run", "--rm"]
    if privileged:
        parts.append("--privileged")
    parts += ["-v", '"$PWD:/src:ro"', BASE_TAG, "bash", "-c", shlex.quote(script)]
    return " ".join(parts)


def run_in_base(script, privileged=False, timeout=900):
    """Выполнить сценарий bash в базовом образе. Возвращает (код, вывод, cmd)."""
    argv = ["docker", "run", "--rm"]
    if privileged:
        argv.append("--privileged")
    argv += ["-v", "%s:/src:ro" % HERE, BASE_TAG, "bash", "-c", script]
    proc = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                          timeout=timeout)
    out = proc.stdout.decode("utf-8", "replace")
    return proc.returncode, out, base_command(script, privileged)


TOOL_VERSIONS = {
    "g++": "g++ --version | head -1",
    "clang++": "clang++ --version | head -1",
    "cmake": "cmake --version | head -1",
    "valgrind": "valgrind --version",
    "perf": "perf --version",
    "llvm-cov": "llvm-cov --version | grep -i 'llvm version'",
    "gtest": "dpkg-query -W -f='${Version}' libgtest-dev",
    "catch2": "dpkg-query -W -f='${Version}' catch2",
    "benchmark": "dpkg-query -W -f='${Version}' libbenchmark-dev",
    "spdlog": "dpkg-query -W -f='${Version}' libspdlog-dev",
    "asio": "dpkg-query -W -f='${Version}' libasio-dev",
}


def tool_versions():
    """Версии инструментов, снятые в собранном образе: {имя: (вывод, cmd)}."""
    script = "; ".join("echo %s=$(%s)" % (k, v) for k, v in TOOL_VERSIONS.items())
    rc, out, cmd = run_in_base(script)
    if rc != 0:
        raise RuntimeError("tool_versions: код %d\n%s" % (rc, out))
    versions = {}
    for line in out.splitlines():
        if "=" in line:
            k, _, v = line.partition("=")
            versions[k.strip()] = v.strip()
    return versions, cmd
