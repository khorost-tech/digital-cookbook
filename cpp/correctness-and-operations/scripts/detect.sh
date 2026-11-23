#!/usr/bin/env bash
#
# detect.sh — одна строка оси 1: собрать программу с дефектом под детектор и
# запустить её. Запускать из каталога стенда.
#
#   bash scripts/detect.sh <детектор> <O0|O2> <программа> [--disasm]
#
# Детекторы: plain, gtest, asan, ubsan, tsan, valgrind, libfuzzer.
# Компиляторы — как в плане 30.09: plain, gtest и valgrind — g++; санитайзеры
# и libFuzzer — clang++. Каждый детектор — своя сборка: ASan и valgrind на
# одном бинарнике несовместимы.
#
# --disasm вместо запуска печатает дизассемблер ТОГО ЖЕ бинарника, который
# получает детектор: по нему scripts/presence.py решает, есть ли дефект в
# сборке. Инструментация меняет то, что выкидывает оптимизатор (UBSan на -O2
# оставляет malloc, который выкидывает обычная сборка clang), поэтому
# присутствие читается не со «сборки того же компилятора», а с этой.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

det="$1"; opt="$2"; prog="$3"; mode="${4:-}"
case "$opt" in O0|O2) ;; *) echo "уровень: O0|O2" >&2; exit 2 ;; esac
src="/src/defects/$prog.cpp"
std="-std=c++23 -g -$opt -pthread"
bin=/tmp/p

case "$det" in
    plain)    build="g++ $std $src -o $bin";  run="$bin" ;;
    gtest)    build="g++ $std $src -o $bin && mkdir -p /tmp/bin \
                 && for f in /src/defects/*.cpp; do n=\$(basename \$f .cpp); \
                    case \$n in defect_test|parser_edge_fuzz) continue;; esac; \
                    g++ $std \$f -o /tmp/bin/\$n; done \
                 && g++ $std /src/defects/defect_test.cpp -lgtest -lgtest_main -o /tmp/t"
              run="DEFECT_BIN_DIR=/tmp/bin /tmp/t --gtest_filter=Defect.$prog" ;;
    asan)     build="clang++ $std -fsanitize=address -fno-omit-frame-pointer $src -o $bin"; run="$bin" ;;
    ubsan)    build="clang++ $std -fsanitize=undefined -fno-sanitize-recover=all $src -o $bin"; run="$bin" ;;
    tsan)     build="clang++ $std -fsanitize=thread $src -o $bin"; run="$bin" ;;
    valgrind) build="g++ $std $src -o $bin"; run="valgrind -q --error-exitcode=99 --leak-check=full $bin" ;;
    libfuzzer)
        [ "$prog" = parser_edge ] || { echo "libFuzzer — только parser_edge" >&2; exit 2; }
        build="clang++ $std -DFUZZING -fsanitize=fuzzer,address $src /src/defects/parser_edge_fuzz.cpp -o $bin"
        run="cd /tmp && $bin -seed=1 -max_total_time=30" ;;
    *) echo "детектор: plain|gtest|asan|ubsan|tsan|valgrind|libfuzzer" >&2; exit 2 ;;
esac

if [ "$mode" = --disasm ]; then
    script="$build && objdump -d --no-show-raw-insn -C $bin"
else
    script="$build && echo '--- запуск ---' && $run"
fi

docker build -q -t cpp-correctness-and-operations-base:latest -f images/Dockerfile.base . >/dev/null
exec docker run --rm -v "$PWD:/src:ro" cpp-correctness-and-operations-base:latest bash -c "$script"
