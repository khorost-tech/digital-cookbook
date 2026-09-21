#!/bin/bash
# Репродуктор опоры 2: компилирует заведомо непроходящий borrow-check и
# записывает stderr + КОД ВОЗВРАТА rustc в borrow-error.txt.
#
# ВАЖНО (граница оболочек Windows->WSL): запускать этот файл как СКРИПТ из
# ЛОГИН-шелла, иначе теряется и PATH (~/.cargo/bin), и код возврата $?:
#     wsl -u khap -- bash -lc 'bash <путь>/borrow_error/capture.sh'
# Инлайновый '$?' в аргументе `bash -lc '... $? ...'` манглится внешней
# оболочкой (превращается в 0/пусто) — поэтому вся логика живёт в файле.
set -u
cd "$(dirname "$0")/.."   # -> корень проекта mm-rust
OUT="borrow_error/borrow-error.txt"

rm -f uaf uaf.exe

ERR="$(rustc --edition 2021 borrow_error/uaf.rs 2>&1 1>/dev/null)"
RC=$?

{
  echo '$ rustc --edition 2021 borrow_error/uaf.rs'
  printf '%s\n' "$ERR"
  echo ''
  echo '$ echo $?'
  echo "${RC}"
  if [ -f uaf ] || [ -f uaf.exe ]; then
    echo '# ВНИМАНИЕ: бинарь СОЗДАН — ожидалась провальная компиляция!'
  else
    echo '# бинарь не создан (компиляция провалена) — это и есть суть опоры 2:'
    echo '# use-after-free ловится компилятором, а не рантаймом.'
  fi
} > "$OUT"

echo "=== rustc exit code = ${RC} (ожидание: 1) ==="
cat "$OUT"

# --- Проверки (fail-loud): опора 2 доказана, только если ВСЕ маркеры на месте. ---
fail=0
[ "$RC" -eq 1 ] || { echo "FAIL: код возврата rustc = ${RC}, ожидался 1" >&2; fail=1; }
printf '%s\n' "$ERR" | grep -q 'error\[E0502\]' || { echo "FAIL: в выводе нет error[E0502]" >&2; fail=1; }
if [ -f uaf ] || [ -f uaf.exe ]; then echo "FAIL: бинарь uaf создан, а не должен" >&2; fail=1; fi
if [ "$fail" -ne 0 ]; then echo "=== ОПОРА 2 НЕ ДОКАЗАНА ===" >&2; exit 1; fi
echo "=== OK: rustc rc=1, E0502 присутствует, бинарь не создан ==="
