#!/usr/bin/env bash
#
# coverage.sh — ось 7: собрать lib/ и тесты clang'ом с покрытием, прогнать
# основной набор и напечатать отчёт llvm-cov по lib/port.cpp.
#
#   bash scripts/coverage.sh <on|off> [report|show|export|boundary]
#     report   — текстовый отчёт по файлу (по умолчанию);
#     export   — сводка в JSON (llvm-cov export -summary-only) — её читает разбор;
#     show     — разметка по строкам: какая ветвь и какое условие MC/DC не покрыты;
#     boundary — прогнать контрольный тест на 65536 вместо отчёта.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
case "$1" in on) p=ON ;; off) p=OFF ;; *) echo "ошибка: on|off" >&2; exit 2 ;; esac
mode="${2:-report}"
flags="-fprofile-instr-generate -fcoverage-mapping -fcoverage-mcdc"
build="cmake -S /src -B /tmp/c -DCMAKE_CXX_COMPILER=clang++ -DPLANTED=$p -DCMAKE_CXX_FLAGS='$flags' >/dev/null \
       && cmake --build /tmp/c -j4 >/dev/null"
cov="cd /tmp/c && LLVM_PROFILE_FILE=t.profraw ./parse_test --gtest_brief=1 >/dev/null \
     && llvm-profdata merge t.profraw -o t.profdata"
case "$mode" in
    report)   tail="$cov && llvm-cov report ./parse_test -instr-profile=t.profdata --show-branch-summary --show-mcdc-summary /src/lib/port.cpp" ;;
    show)     tail="$cov && llvm-cov show ./parse_test -instr-profile=t.profdata --show-branches=count --show-mcdc /src/lib/port.cpp" ;;
    export)   tail="$cov && llvm-cov export ./parse_test -instr-profile=t.profdata -summary-only /src/lib/port.cpp" ;;
    boundary) tail="/tmp/c/port_boundary_test --gtest_brief=1" ;;
    *) echo "режим: report|show|export|boundary" >&2; exit 2 ;;
esac
docker build -q -t cpp-correctness-and-operations-base:latest -f images/Dockerfile.base . >/dev/null
exec docker run --rm -v "$PWD:/src:ro" cpp-correctness-and-operations-base:latest bash -c "$build && $tail"
