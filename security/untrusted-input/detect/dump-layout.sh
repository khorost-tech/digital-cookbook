#!/usr/bin/env bash
# Дамп заголовка соседнего чанка до и после OOB-записи.
#
# Именно этот дамп объясняет, почему порог у baseline glibc проходит между
# 9 и 10 байтами: рассуждения про malloc_usable_size недостаточно — важно,
# КАКОЕ значение оказывается в поле размера соседа и выглядит ли оно
# правдоподобно для внутренней проверки аллокатора.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

BIN=./bin/heap-dump
OUT=./results/heap-layout.txt

[ -x "$BIN" ] || { echo "нет $BIN — запустите ./build.sh"; exit 1; }
mkdir -p results

{
  echo "# Дамп заголовка соседнего чанка при разных смещениях OOB"
  echo "# malloc(32); окружение — см. results/env.txt"
  echo
  for n in 4 8 9 10 12; do
    echo "--- OOB $n байт ---"
    "$BIN" "$n"
    echo
  done
} > "$OUT"

cat "$OUT"
