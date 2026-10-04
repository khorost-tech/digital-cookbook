#!/usr/bin/env bash
# Матрица «механизм детекта × размер выхода за границу».
#
# Варьируется ОДНА переменная — на сколько байт запись уходит за границу блока.
# Всё остальное фиксировано: одна программа, один уровень оптимизации, одна
# машина, один размер блока (32 байта).
#
# Сырой вывод каждой ячейки сохраняется в results/raw/ — любую строку сводки
# можно перепроверить руками.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

BIN=./bin/oobtest
ASAN=./bin/oobtest-asan
SCUDO=./bin/oobtest-scudo
HM=./bin/libhardened_malloc.so

RESULTS=./results
RAW="$RESULTS/raw"
mkdir -p "$RAW"

# КРИТИЧНО. Начиная с glibc 2.34 отладочные проверки malloc вынесены в отдельную
# библиотеку libc_malloc_debug. Без её предзагрузки переменные MALLOC_CHECK_ и
# glibc.malloc.check НЕ ДЕЙСТВУЮТ — процесс ведёт себя ровно как baseline.
# Пропустить это очень легко: матрица заполнится, а по факту baseline будет
# сравниваться сам с собой, и вывод «проверки ничего не меняют» окажется ложным.
MALLOC_DEBUG="$(ldconfig -p 2>/dev/null | awk '/libc_malloc_debug\.so\.0/ {print $NF; exit}')"

# Fail-fast: пустой путь дал бы LD_PRELOAD="" и молча вернул нас к тому же
# сравнению baseline с baseline. Мало найти путь — надо убедиться, что
# предзагрузка РЕАЛЬНО включает проверки. Проверяем заведомо битым входом.
verify_malloc_debug() {
  if [ -z "$MALLOC_DEBUG" ] || [ ! -f "$MALLOC_DEBUG" ]; then
    log "ОСТАНОВКА: libc_malloc_debug.so.0 не найдена (ldconfig -p | grep malloc_debug)"
    log "  Без неё MALLOC_CHECK_ и glibc.malloc.check не действуют, и матрица соврёт."
    return 1
  fi

  # Smoke test: выход за границу на 1 байт должен быть замечен.
  local out rc
  out="$(env LD_PRELOAD="$MALLOC_DEBUG" MALLOC_CHECK_=3 "$BIN" 1 2>&1)"; rc=$?
  {
    echo "libc_malloc_debug: $MALLOC_DEBUG"
    echo "smoke test (OOB 1 байт, MALLOC_CHECK_=3): exit_code=$rc"
    echo "вывод: $(printf '%s' "$out" | head -1)"
  } > "$RESULTS/malloc-debug-smoke.txt"

  # Ненулевого кода мало: он бывает и при сбое загрузчика. Требуем ещё и
  # характерную диагностику аллокатора — иначе «проверки работают» может
  # означать «предзагрузка сломала запуск».
  if [ "$rc" -eq 0 ]; then
    log "ОСТАНОВКА: предзагрузка не включила проверки — однобайтовый выход не замечен"
    log "  Проверьте $MALLOC_DEBUG и версию glibc."
    echo "ВЕРДИКТ: проверки НЕ включились" >> "$RESULTS/malloc-debug-smoke.txt"
    return 1
  fi

  if ! printf '%s' "$out" | grep -qiE 'malloc|free\(\)|corrupted|invalid pointer'; then
    log "ОСТАНОВКА: ненулевой код, но без диагностики аллокатора — похоже на сбой запуска"
    echo "ВЕРДИКТ: ненулевой код без диагностики — считать проверки включёнными нельзя" \
      >> "$RESULTS/malloc-debug-smoke.txt"
    return 1
  fi

  echo "ВЕРДИКТ: проверки включены и работают" >> "$RESULTS/malloc-debug-smoke.txt"
  log "libc_malloc_debug: $MALLOC_DEBUG (smoke test пройден, rc=$rc)"
  return 0
}

# Все бинарники должны существовать ДО прогона: иначе их отсутствие проявится
# как ненулевой код в ячейке матрицы.
verify_binaries() {
  local missing=0 b
  for b in "$BIN" "$ASAN" "$SCUDO"; do
    if [ ! -x "$b" ]; then
      log "ОСТАНОВКА: нет исполняемого $b — запустите ./build.sh"
      missing=1
    fi
  done
  [ "$missing" -eq 0 ]
}

# Смещения: от «чуть-чуть за границей» до «затёрли соседние структуры».
# Значения 8/9/10 выбраны не случайно — именно там проходит порог: malloc(32)
# на glibc 2.39 фактически отдаёт 40 байт, поэтому первые 8 байт «за границей»
# ещё лежат внутри выделенной области (см. results/env.txt).
OFFSETS=(1 4 8 9 10 16 64 512)

[ -x "$BIN" ] || { echo "нет $BIN — запустите ./build.sh"; exit 1; }

log() { printf '[matrix] %s\n' "$*" >&2; }

# Запускает одну ячейку. Печатает вердикт одним словом.
# $1 — метка, $2 — смещение, $3 — бинарник, далее — переменные окружения.
run_cell() {
  local label="$1" off="$2" bin="$3"; shift 3
  local raw="$RAW/${label}-${off}.txt"
  local out rc

  out="$(env "$@" "$bin" "$off" 2>&1)"; rc=$?
  printf '%s\n' "$out" > "$raw"
  printf 'exit_code=%s\n' "$rc" >> "$raw"

  # Три исхода, а не два. Считать детектом ЛЮБОЙ ненулевой код нельзя:
  # отсутствующий бинарник, сбой загрузчика или несовместимая библиотека тоже
  # дают ненулевой код — и молча превратились бы в «поймал», завысив картину.
  # Поэтому детект подтверждается характерной диагностикой аллокатора.
  if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'finished normally'; then
    echo "молчит"
  elif printf '%s' "$out" | grep -qiE 'malloc|free\(\)|corrupted|canary|AddressSanitizer|Scudo|invalid pointer|allocator'; then
    echo "поймал"
  else
    # Ненулевой код без внятной диагностики — это сбой запуска, а не находка.
    printf 'ВНИМАНИЕ: %s@%s — ненулевой код (%s) без диагностики аллокатора\n' \
      "$label" "$off" "$rc" >&2
    echo "ошибка"
  fi
}

# Первая строка диагностики механизма — для колонки «что сказал».
diag() {
  local label="$1" off="$2"
  grep -viE '^(finished|verify:|exit_code)' "$RAW/${label}-${off}.txt" 2>/dev/null \
    | grep -viE '^(=====|$)' | head -1 | cut -c1-70
}

if ! verify_binaries; then
  exit 1
fi

# Проверки glibc обязаны быть реально включены — иначе матрица соврёт.
if ! verify_malloc_debug; then
  exit 1
fi

log "контроль: убеждаемся, что OOB-запись реально происходит"
: > "$RESULTS/control-write-visible.txt"
for off in "${OFFSETS[@]}"; do
  "$BIN" "$off" verify 2>&1 | grep '^verify:' >> "$RESULTS/control-write-visible.txt" || true
done
cat "$RESULTS/control-write-visible.txt" | sed 's/^/[matrix]   /' >&2

# Если запись не видна, вся матрица бессмысленна: «молчит» будет означать
# «писать было нечего», а не «механизм не заметил».
#
# Проверять только наличие строки «no» недостаточно: если процесс упал ДО вывода
# контроля, строки не будет вовсе — и такая проверка молча пройдёт. Поэтому
# требуем ровно одну строку «yes» на каждое смещение, без пропусков и дубликатов.
control_ok=1
for off in "${OFFSETS[@]}"; do
  n_yes=$(grep -c "oob_write_visible=yes bytes=${off}\$" "$RESULTS/control-write-visible.txt")
  n_any=$(grep -c "bytes=${off}\$" "$RESULTS/control-write-visible.txt")
  if [ "$n_any" -eq 0 ]; then
    log "ОСТАНОВКА: для смещения $off контроль отсутствует (процесс упал до вывода?)"
    control_ok=0
  elif [ "$n_any" -gt 1 ]; then
    log "ОСТАНОВКА: для смещения $off контроль встречается $n_any раз — дубликаты"
    control_ok=0
  elif [ "$n_yes" -ne 1 ]; then
    log "ОСТАНОВКА: для смещения $off запись НЕ наблюдается"
    control_ok=0
  fi
done

if [ "$control_ok" -ne 1 ]; then
  log "Матрица не строится: без подтверждённой записи её ячейки ничего не значат."
  exit 1
fi
log "контроль пройден: ${#OFFSETS[@]} смещений, по одной подтверждённой записи на каждое"

# Матрица пишется скриптом в файл, а не через перенаправление stdout вызывающим.
# Иначе легко получить рассинхрон: сырые артефакты от нового прогона, а
# results/matrix.tsv — от старого. Один прогон = один согласованный снимок.
MATRIX="$RESULTS/matrix.tsv"
printf 'offset\tbaseline\tmalloc_check\ttunables\tperturb\thardened\tscudo\tasan\n' > "$MATRIX"
errors=0

for off in "${OFFSETS[@]}"; do
  log "смещение $off байт"
  b=$(run_cell baseline     "$off" "$BIN")
  # MALLOC_CHECK_ и glibc.malloc.check живут в libc_malloc_debug — их запускаем
  # с предзагрузкой.
  m=$(run_cell malloc_check "$off" "$BIN" LD_PRELOAD="$MALLOC_DEBUG" MALLOC_CHECK_=3)
  t=$(run_cell tunables     "$off" "$BIN" LD_PRELOAD="$MALLOC_DEBUG" GLIBC_TUNABLES=glibc.malloc.check=3)
  # А MALLOC_PERTURB_ — БЕЗ неё: он реализован в самом libc и предзагрузки не
  # требует. Добавить сюда LD_PRELOAD значило бы менять две переменные разом и
  # приписывать perturbation чужой эффект.
  p=$(run_cell perturb      "$off" "$BIN" MALLOC_PERTURB_=42)
  if [ -f "$HM" ]; then
    h=$(run_cell hardened   "$off" "$BIN" LD_PRELOAD="$HM")
  else
    h="н/д"
  fi
  s=$(run_cell scudo        "$off" "$SCUDO")
  a=$(run_cell asan         "$off" "$ASAN")
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$off" "$b" "$m" "$t" "$p" "$h" "$s" "$a" >> "$MATRIX"

  # Ячейка «ошибка» означает сбой запуска, а не результат наблюдения. Считаем
  # такие случаи: сохранить матрицу с ними и отчитаться об успехе значило бы
  # выдать сломанный прогон за данные.
  for cell in "$b" "$m" "$t" "$p" "$h" "$s" "$a"; do
    [ "$cell" = "ошибка" ] && errors=$((errors + 1))
  done
done

log "диагностические сообщения (что именно сказал механизм):"
{
  echo
  echo "# Что сказали механизмы, которые поймали"
  for off in "${OFFSETS[@]}"; do
    for lbl in baseline malloc_check tunables perturb hardened scudo asan; do
      d="$(diag "$lbl" "$off")"
      [ -n "$d" ] && printf '%s\t%s\t%s\n' "$off" "$lbl" "$d"
    done
  done
} > "$RESULTS/diagnostics.tsv"

if [ "$errors" -gt 0 ]; then
  log "ОСТАНОВКА: $errors ячеек завершились сбоем запуска (см. ВНИМАНИЕ выше и $RAW)"
  log "  Матрица сохранена для разбора, но результат прогона НЕДОСТОВЕРЕН."
  printf '# ВНИМАНИЕ: %s ячеек — ошибка запуска, результат недостоверен\n' "$errors" >> "$MATRIX"
  exit 1
fi

log "готово: матрица в results/matrix.tsv, сырой вывод в $RAW, диагностика в $RESULTS/diagnostics.tsv"
