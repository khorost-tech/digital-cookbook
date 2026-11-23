#!/usr/bin/env bash
#
# disasm-bench.sh — собрать тело бенчмарка оси 2 и напечатать дизассемблер
# одной функции. Бенчмарк не запускается. Запускать из каталога стенда.
#
#   bash scripts/disasm-bench.sh <g++|clang++> <O0|O2|O3> <функция>
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
cc="$1"; opt="$2"; fn="$3"
case "$cc" in g++|clang++) ;; *) echo "компилятор: g++|clang++" >&2; exit 2 ;; esac
case "$opt" in O0|O2|O3) ;; *) echo "уровень: O0|O2|O3" >&2; exit 2 ;; esac
docker build -q -t cpp-correctness-and-operations-base:latest -f images/Dockerfile.base . >/dev/null
exec docker run --rm -v "$PWD:/src:ro" cpp-correctness-and-operations-base:latest bash -c \
    "$cc -std=c++23 -$opt /src/bench/body.cpp -lbenchmark -lpthread -o /tmp/b \
     && objdump -d --no-show-raw-insn -C /tmp/b | awk '/<$fn\\(benchmark::State&\\)>:/,/^\$/'"
