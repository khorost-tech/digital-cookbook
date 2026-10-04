#!/bin/sh
# build.sh — сборка, запуск И ПРОВЕРКА C++-части стенда «управление памятью».
#
# На хосте C-компилятора может не быть: собираем и запускаем в образе gcc:13.
# Флаги контейнера --cap-add=SYS_PTRACE --security-opt seccomp=unconfined нужны,
# чтобы LeakSanitizer мог сканировать процесс.
#
# Путь монтирования берётся из РАСПОЛОЖЕНИЯ скрипта (портабельно: Linux/macOS и
# Git Bash на Windows; MSYS_NO_PATHCONV выставляется ниже).
#
# FAIL-LOUD И САМОПРОВЕРКА: скрипт не просто печатает вывод, а ПРОВЕРЯЕТ ожидаемые
# маркеры каждого сценария и код возврата. Любое расхождение (нет маркера, не тот
# код, упавшая компиляция) -> ненулевой выход. «Демонстрация дефекта» здесь = тест.
#
# Использование: sh build.sh
set -eu
export MSYS_NO_PATHCONV=1

SCRIPT_DIR="$(cd "$(dirname "$0")" && (pwd -W 2>/dev/null || pwd))"
IMAGE='gcc:13'

run_in_container() {
    docker run --rm \
        --cap-add=SYS_PTRACE --security-opt seccomp=unconfined \
        -v "${SCRIPT_DIR}:/src" -w /src \
        "${IMAGE}" sh -c "$1"
}

# compile_run <name>: печатает вывод программы + строку "program-exit=N".
# Падение КОМПИЛЯЦИИ роняет скрипт (exit 42 из контейнера -> set -e на $(...)).
compile_run() {
    run_in_container "
        g++ -std=c++20 -g -fsanitize=address $1.cpp -o /tmp/app \
            || { echo '!!! КОМПИЛЯЦИЯ $1.cpp УПАЛА'; exit 42; }
        ASAN_OPTIONS=detect_leaks=1 /tmp/app 2>&1
        echo \"program-exit=\$?\"
    "
}

FAIL=0
# check <name> <want:nonzero|zero|clean> <маркер>...
#   nonzero — программа должна выйти НЕнулевым кодом (санитайзер нашёл дефект);
#   zero    — нулевым;
#   clean   — нулевым И без строки "ERROR" (санитайзер чист).
check() {
    name="$1"; want="$2"; shift 2
    echo "=== ${name} ==="
    out="$(compile_run "$name")"
    echo "$out"
    ec="$(printf '%s\n' "$out" | sed -n 's/^program-exit=//p' | tail -1)"
    ok=1
    for m in "$@"; do
        printf '%s' "$out" | grep -q -- "$m" || { echo "  FAIL: нет ожидаемого маркера: $m" >&2; ok=0; }
    done
    case "$want" in
        nonzero) [ "${ec:-0}" -ne 0 ] || { echo "  FAIL: program-exit=$ec, ожидался НЕнулевой" >&2; ok=0; } ;;
        zero)    [ "${ec:-1}" -eq 0 ] || { echo "  FAIL: program-exit=$ec, ожидался 0" >&2; ok=0; } ;;
        clean)   [ "${ec:-1}" -eq 0 ] || { echo "  FAIL: program-exit=$ec, ожидался 0" >&2; ok=0; }
                 printf '%s' "$out" | grep -q 'ERROR' && { echo "  FAIL: санитайзер не чист (есть ERROR)" >&2; ok=0; } || true ;;
    esac
    if [ "$ok" -eq 1 ]; then echo "  OK (${name})"; else FAIL=1; fi
    echo
}

echo '=== g++ --version (контрольная величина тулчейна) ==='
run_in_container 'g++ --version'
echo

# Опора 1: цикл shared_ptr течёт — 0 деструкторов + отчёт LeakSanitizer, ненулевой код.
check cycle_leak  nonzero 'destructors called: 0' 'LeakSanitizer: detected memory leaks'
# Опора 1: weak_ptr чинит — 2 деструктора, санитайзер чист, нулевой код.
check cycle_fixed clean   'destructors called: 2'
# Опора 2: use-after-free ловится ASan в рантайме, ненулевой код.
check uaf         nonzero 'heap-use-after-free'

if [ "$FAIL" -ne 0 ]; then
    echo '!!! ЕСТЬ ПРОВАЛЫ — стенд НЕ доказан' >&2
    exit 1
fi
echo 'ВСЕ СЦЕНАРИИ ПРОШЛИ ПРОВЕРКУ'
