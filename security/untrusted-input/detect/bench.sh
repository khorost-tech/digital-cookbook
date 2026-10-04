#!/usr/bin/env bash
# Сколько стоит детект: время и пиковая память под нагрузкой без OOB.
#
# Меряется одна и та же программа в режиме bench (цикл аллокаций и записей),
# отличается только механизм. Берётся медиана из нескольких прогонов — разброс
# на живой машине неизбежен, и одиночный замер ввёл бы в заблуждение.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

BIN=./bin/oobtest
ASAN=./bin/oobtest-asan
SCUDO=./bin/oobtest-scudo
HM=./bin/libhardened_malloc.so

# Проверки malloc с glibc 2.34+ живут в отдельной библиотеке: без её
# предзагрузки MALLOC_CHECK_ и glibc.malloc.check не включаются, и замер
# «стоимости проверок» на самом деле трижды померил бы baseline.
MALLOC_DEBUG="$(ldconfig -p 2>/dev/null | awk '/libc_malloc_debug\.so\.0/ {print $NF; exit}')"

ITER="${ITER:-2000000}"
REPEATS="${REPEATS:-5}"

[ -x "$BIN" ] || { echo "нет $BIN — запустите ./build.sh"; exit 1; }

# Пустой путь дал бы LD_PRELOAD="" и мы бы померили baseline вместо проверок.
if [ -z "$MALLOC_DEBUG" ] || [ ! -f "$MALLOC_DEBUG" ]; then
  echo "ОСТАНОВКА: libc_malloc_debug.so.0 не найдена — замер проверок glibc невозможен" >&2
  exit 1
fi

log() { printf '[bench] %s\n' "$*" >&2; }

# Возвращает "секунды<TAB>пик_RSS_КБ" для одного прогона.
# Ненулевой код возврата означает сбой: агрегировать такое нельзя, сломанный
# прогон попал бы в медиану и выглядел бы как обычный замер.
#
# Замер пишется в отдельный файл через -o, а не читается из конвейера. Причин две:
#   1. При `out="$(... | tail -1)"` статус конвейера ненадёжен: PIPESTATUS
#      относится к последнему конвейеру текущего shell, а не к тому, что
#      выполнялся внутри подстановки. Проверено — часть сбоев так пропускалась.
#   2. `2>&1 >/dev/null` смешивает stderr самой программы с выводом time,
#      и `tail -1` может взять чужую строку вместо замера.
# Прямая проверка кода возврата команды снимает обе проблемы.
measure_once() {
  local bin="$1"; shift
  local out timing
  timing="$(mktemp)"

  if ! /usr/bin/time -o "$timing" -f '%e\t%M' env "$@" "$bin" bench "$ITER" >/dev/null 2>&1; then
    log "  прогон завершился с ошибкой — замер отброшен"
    rm -f "$timing"
    return 1
  fi

  out="$(cat "$timing")"
  rm -f "$timing"

  # Поля должны быть числами: "0.31" и "1912". Иначе в таблицу уедет мусор.
  local t r
  t="$(printf '%s' "$out" | cut -f1)"
  r="$(printf '%s' "$out" | cut -f2)"
  case "$t" in ''|*[!0-9.]*) log "  некорректное время: '$t'"; return 1 ;; esac
  case "$r" in ''|*[!0-9]*)  log "  некорректный RSS: '$r'";  return 1 ;; esac

  printf '%s\t%s\n' "$t" "$r"
}

# Агрегаты по REPEATS прогонам. КАЖДЫЙ отдельный прогон пишется в raw-файл:
# по одному агрегату нельзя проверить ни число повторов, ни разброс, ни то,
# что медиана посчитана верно. Для статьи, где «сколько это стоит» — главный
# вопрос, сырые замеры обязательны.
#
# Время берётся МЕДИАНОЙ, а пиковая RSS — МАКСИМУМОМ (пик есть пик).
# Это разные агрегаты, и в таблице они подписаны по-разному.
measure() {
  local label="$1" bin="$2"; shift 2
  local times=() rss=() out t r
  local run=0
  for _ in $(seq "$REPEATS"); do
    run=$((run + 1))
    if ! out="$(measure_once "$bin" "$@")"; then
      printf '%s\t%s\tОШИБКА\tОШИБКА\n' "$label" "$run" >> "$RAW_BENCH"
      continue
    fi
    t="$(printf '%s' "$out" | cut -f1)"
    r="$(printf '%s' "$out" | cut -f2)"
    times+=("$t")
    rss+=("$r")
    printf '%s\t%s\t%s\t%s\n' "$label" "$run" "$t" "$r" >> "$RAW_BENCH"
  done

  # Агрегировать неполную серию нельзя: медиана из трёх замеров вместо пяти
  # выглядела бы в таблице так же убедительно, как из пяти.
  if [ "${#times[@]}" -ne "$REPEATS" ]; then
    log "  $label: получено ${#times[@]} из $REPEATS замеров — строка помечена как недостоверная"
    printf '%s\tНЕДОСТОВЕРНО\tНЕДОСТОВЕРНО\tполучено %s из %s\n' \
      "$label" "${#times[@]}" "$REPEATS"
    BENCH_ERRORS=$((BENCH_ERRORS + 1))
    return 0
  fi
  local median_t max_r min_t
  median_t=$(printf '%s\n' "${times[@]}" | sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}')
  max_r=$(printf '%s\n' "${rss[@]}" | sort -n | tail -1)
  min_t=$(printf '%s\n' "${times[@]}" | sort -n | head -1)
  local spread_t
  spread_t=$(printf '%s\n' "${times[@]}" | sort -n | tail -1)
  printf '%s\t%s\t%s\t%s..%s\n' "$label" "$median_t" "$max_r" "$min_t" "$spread_t"
}

RESULTS=./results
RAW_BENCH="$RESULTS/overhead-raw.tsv"
mkdir -p "$RESULTS"
BENCH_ERRORS=0

# Параметры прогона фиксируются в самом артефакте — иначе «20 млн итераций,
# 5 повторов» остаётся утверждением статьи, не проверяемым по данным.
{
  printf '# ITER=%s REPEATS=%s\n' "$ITER" "$REPEATS"
  printf '# запуск: %s\n' "$(date -u '+%Y-%m-%d %H:%M UTC')"
  printf 'механизм\tповтор\tвремя_с\tпик_RSS_КБ\n'
} > "$RAW_BENCH"

log "нагрузка: $ITER итераций, повторов: $REPEATS"

{
  printf '# ITER=%s REPEATS=%s; время — медиана, RSS — максимум; сырые прогоны в overhead-raw.tsv\n' "$ITER" "$REPEATS"
  printf 'механизм\tвремя_медиана_с\tпик_RSS_макс_КБ\tразброс_времени_с\n'
  measure "baseline"      "$BIN"
  measure "malloc_check"  "$BIN" LD_PRELOAD="$MALLOC_DEBUG" MALLOC_CHECK_=3
  measure "tunables"      "$BIN" LD_PRELOAD="$MALLOC_DEBUG" GLIBC_TUNABLES=glibc.malloc.check=3
  # perturb — БЕЗ предзагрузки: он реализован в libc и её не требует. Иначе в
  # его строке смешались бы два фактора и цена preload приписалась бы ему.
  measure "perturb"       "$BIN" MALLOC_PERTURB_=42
  if [ -f "$HM" ]; then
    measure "hardened"    "$BIN" LD_PRELOAD="$HM"
  fi
  measure "scudo"         "$SCUDO"
  measure "asan"          "$ASAN"
}

if [ "$BENCH_ERRORS" -gt 0 ]; then
  log "ОСТАНОВКА: $BENCH_ERRORS механизмов не набрали $REPEATS корректных замеров"
  log "  Их строки помечены НЕДОСТОВЕРНО; см. $RAW_BENCH"
  exit 1
fi

log "готово: $REPEATS корректных замеров на каждый механизм"
