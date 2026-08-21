#!/usr/bin/env bash
# Сборка трёх вариантов учебной программы и hardened_malloc.
#
# Уровень оптимизации фиксирован (-O2): он влияет на раскладку и на то, что
# компилятор делает с записями, поэтому «одна переменная за раз» требует его
# закрепить. Записи защищены volatile — см. комментарий в oobtest.c.

set -euo pipefail
cd "$(dirname "$0")"

CFLAGS="-O2 -g -fno-omit-frame-pointer"
HM_COMMIT="${HM_COMMIT:-main}"

log() { printf '[build] %s\n' "$*" >&2; }

command -v gcc   >/dev/null || { echo "нужен gcc"; exit 1; }
command -v clang >/dev/null || { echo "нужен clang (для ASAN и scudo)"; exit 1; }

log "gcc:   $(gcc --version | head -1)"
log "clang: $(clang --version | head -1)"
log "glibc: $(ldd --version | head -1)"

mkdir -p bin

log "сборка baseline (gcc)"
# shellcheck disable=SC2086
gcc $CFLAGS -o bin/oobtest oobtest.c

log "сборка ASAN (clang)"
# shellcheck disable=SC2086
clang $CFLAGS -fsanitize=address -o bin/oobtest-asan oobtest.c

log "сборка scudo (clang)"
# shellcheck disable=SC2086
clang $CFLAGS -fsanitize=scudo -o bin/oobtest-scudo oobtest.c

# Без оптимизации: программа читает заголовок соседнего чанка, и оптимизатор
# не должен переупорядочивать эти обращения.
log "сборка heap-dump (gcc -O0)"
gcc -O0 -g -o bin/heap-dump heap-dump.c

log "сборка arena-info (gcc -O0)"
gcc -O0 -g -o bin/arena-info arena-info.c

# hardened_malloc собирается ТОЛЬКО clang'ом: проект требует -std=c23,
# а gcc 13.3 знает лишь -std=c2x и падает на этом флаге.
if [ ! -f bin/libhardened_malloc.so ]; then
  log "сборка hardened_malloc (clang, коммит $HM_COMMIT)"
  rm -rf .hm
  if git clone -q --depth 1 https://github.com/GrapheneOS/hardened_malloc.git .hm 2>/dev/null; then
    (cd .hm && git rev-parse HEAD > ../bin/hardened_malloc.commit)
    if (cd .hm && make CC=clang CXX=clang++ -j"$(nproc)" >/dev/null 2>&1); then
      cp .hm/out/libhardened_malloc.so bin/
      log "hardened_malloc собран: $(cat bin/hardened_malloc.commit)"
    else
      log "ВНИМАНИЕ: hardened_malloc не собрался — его строки в матрице будут пропущены"
    fi
    rm -rf .hm
  else
    log "ВНИМАНИЕ: не удалось клонировать hardened_malloc (нет сети?)"
  fi
fi

# Фиксация окружения: без версий компилятора, glibc и коммита hardened_malloc
# числа прогона невоспроизводимы — порог детекта зависит от раскладки аллокатора.
mkdir -p results
{
  echo "# Окружение сборки"
  echo
  echo "Дата: $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo "Ядро: $(uname -sr)"
  echo "Архитектура: $(uname -m)"
  echo
  echo "gcc:   $(gcc --version | head -1)"
  echo "clang: $(clang --version | head -1)"
  echo "glibc: $(ldd --version | head -1)"
  echo
  echo "CFLAGS: $CFLAGS"
  if [ -f bin/hardened_malloc.commit ]; then
    echo "hardened_malloc commit: $(cat bin/hardened_malloc.commit)"
  else
    echo "hardened_malloc: не собран"
  fi
  echo
  echo "# Механизмы детекта"
  echo "libc_malloc_debug: $(ldconfig -p 2>/dev/null | awk '/libc_malloc_debug\.so\.0/ {print $NF; exit}')"
  echo "  (без её предзагрузки MALLOC_CHECK_ и glibc.malloc.check НЕ действуют;"
  echo "   результат smoke test — в results/malloc-debug-smoke.txt)"
  echo "MALLOC_PERTURB_ предзагрузки не требует — запускается без LD_PRELOAD."
  echo "ASAN_OPTIONS: ${ASAN_OPTIONS:-(не задан, используются значения по умолчанию)}"
  echo
  echo "# Раскладка аллокатора (объясняет порог детекта)"
  echo "malloc(32) фактически выделяет:"
} > results/env.txt

# Печатаем usable_size: именно зазор между запрошенным и фактическим размером
# создаёт зону, где запись «за границей» никем не замечается.
cat > /tmp/usable.c <<'USABLE'
#include <stdio.h>
#include <stdlib.h>
#include <malloc.h>
int main(void) {
    void *p = malloc(32);
    printf("  malloc_usable_size(malloc(32)) = %zu байт\n", malloc_usable_size(p));
    printf("  запас на выравнивание = %zu байт\n", malloc_usable_size(p) - 32);
    free(p);
    return 0;
}
USABLE
if gcc -O2 -o /tmp/usable /tmp/usable.c 2>/dev/null; then
  /tmp/usable >> results/env.txt
fi

log "готово:"
find bin -maxdepth 1 -type f -printf '[build]   %-28f %8s байт\n' | sort
log "окружение записано в results/env.txt"
