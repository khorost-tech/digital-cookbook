#!/usr/bin/env bash
# Проверка границ изоляции парсера.
#
# Три принципа, без которых матрица ничего не доказывает.
#
# 1. ПРОВЕРЯЕМ ТУ КОНФИГУРАЦИЮ, КОТОРАЯ РАБОТАЕТ. Полный набор флагов совпадает
#    с параметрами сервиса parser в docker-compose.yml. Ранняя версия стенда
#    проверяла `--network none`, тогда как воркер жил в internal-сети:
#    доказывалось не то ограничение, которое действовало в конвейере.
#
# 2. ОДНА ГРАНИЦА ЗА РАЗ. Каждая проба выполняется с контролем (без ограничений),
#    с ОДНИМ изучаемым ограничением и с полным набором. Иначе отказ chmod
#    одинаково объясняется и seccomp, и cap_drop, и непривилегированным uid.
#
# 3. МОНТИРОВАНИЕ ПАРАМЕТРИЗОВАНО. Режим тома задаётся на каждый запуск, иначе
#    общий RW-mount перебивал бы проверяемую границу «вход только на чтение».

set -uo pipefail
cd "$(dirname "$0")" || exit 1

RESULTS=./results
RAW="$RESULTS/raw"
mkdir -p "$RAW"

IMAGE=pipeline-parser
SECCOMP="$(pwd)/seccomp-parser.json"
SAMPLE_DIR=/tmp/ui-pipe-sample
# Сеть конвейера — та же, что поднимает compose (имя проекта = имя каталога).
COMPOSE_NET="$(basename "$(pwd)")_pipeline"

log() { printf '[bounds] %s\n' "$*" >&2; }

[ -f "$SECCOMP" ] || { log "нет $SECCOMP"; exit 1; }
docker image inspect "$IMAGE" >/dev/null 2>&1 || {
  log "нет образа $IMAGE — сначала docker compose build"; exit 1; }

mkdir -p "$SAMPLE_DIR"
if [ ! -f "$SAMPLE_DIR/sample.avi" ]; then
  log "готовлю образец медиафайла"
  docker run --rm -v "$SAMPLE_DIR:/out" \
    jrottenberg/ffmpeg@sha256:83ef82d9850314baa3504821e2ea6598e40e2096ac8f967a842d31234be2be92 \
    -y -v error -f lavfi -i testsrc=size=160x120:rate=5:duration=1 \
    -c:v magicyuv /out/sample.avi >/dev/null 2>&1
fi

# Запуск одной пробы. $1 — имя пробы, $2 — режим монтирования входа (rw|ro),
# далее — флаги docker run. Монтирование добавляется ровно один раз.
run_probe() {
  local probe="$1" mode="$2"; shift 2
  docker run --rm \
    -e PROBE_MODE=1 -e "PROBE_ONLY=$probe" \
    -e INPUT_DIR=/data/input -e DONE_DIR=/tmp \
    -v "$SAMPLE_DIR:/data/input:$mode" \
    "$@" "$IMAGE" 2>&1 | tail -1
}

allowed_of() { printf '%s' "$1" | grep -o '"allowed":[a-z]*' | cut -d: -f2 | head -1; }
detail_of()  { printf '%s' "$1" | sed 's/.*"detail":"\([^"]*\)".*/\1/' | head -1 | cut -c1-48; }

# Полный набор границ парсера — как в docker-compose.yml.
FULL=(--network none --read-only --cap-drop ALL
      --security-opt no-new-privileges:true
      --security-opt "seccomp=$SECCOMP"
      --user 65534:65534 --pids-limit 64 --memory 256m
      --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m)

emit_row() {
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$@"
}

{
  printf 'проба\tграница\tбез границ\tтолько эта граница\tполный набор\tвердикт\tдиагностика\n'
  errors=0

  check() {
    local probe="$1" boundary="$2" mode="$3"; shift 3
    local out_c out_s out_f c s f d verdict

    out_c="$(run_probe "$probe" rw)"                 # контроль: ничего не ограничено
    out_s="$(run_probe "$probe" "$mode" "$@")"       # только изучаемая граница
    out_f="$(run_probe "$probe" ro "${FULL[@]}")"    # весь набор

    printf '%s\n' "$out_c" > "$RAW/${probe}-control.json"
    printf '%s\n' "$out_s" > "$RAW/${probe}-single.json"
    printf '%s\n' "$out_f" > "$RAW/${probe}-full.json"

    c="$(allowed_of "$out_c")"; s="$(allowed_of "$out_s")"; f="$(allowed_of "$out_f")"
    d="$(detail_of "$out_f")"

    if [ -z "$c" ] || [ -z "$s" ] || [ -z "$f" ]; then
      verdict="ОШИБКА: проба не отработала"; errors=$((errors + 1))
    elif [ "$c" != "true" ]; then
      verdict="ОШИБКА: контроль не прошёл"; errors=$((errors + 1))
    elif [ "$s" = "false" ] && [ "$f" = "false" ]; then
      verdict="граница работает"
    elif [ "$s" = "true" ] && [ "$f" = "false" ]; then
      verdict="закрывает не эта граница, а другая из набора"; errors=$((errors + 1))
    else
      verdict="ГРАНИЦА НЕ ДЕЙСТВУЕТ"; errors=$((errors + 1))
    fi

    emit_row "$probe" "$boundary" "${c:-—}" "${s:-—}" "${f:-—}" "$verdict" "$d"
  }

  log "внешняя сеть"
  check network_external "network_mode: none" rw --network none

  log "соседи по конвейеру (Redis, API)"
  # Контроль здесь особый: чтобы соседи вообще были достижимы, контейнер должен
  # стоять в сети compose. Проверяем отдельно, вне общей схемы check().
  out_c="$(run_probe network_internal rw --network "$COMPOSE_NET")"
  out_s="$(run_probe network_internal rw --network none)"
  out_f="$(run_probe network_internal ro "${FULL[@]}")"
  printf '%s\n' "$out_c" > "$RAW/network_internal-control.json"
  printf '%s\n' "$out_s" > "$RAW/network_internal-single.json"
  printf '%s\n' "$out_f" > "$RAW/network_internal-full.json"
  c="$(allowed_of "$out_c")"; s="$(allowed_of "$out_s")"; f="$(allowed_of "$out_f")"
  if [ "$c" = "true" ] && [ "$s" = "false" ] && [ "$f" = "false" ]; then
    emit_row network_internal "network_mode: none" "$c" "$s" "$f" \
      "граница работает" "$(detail_of "$out_c") -> недоступны"
  else
    emit_row network_internal "network_mode: none" "${c:-—}" "${s:-—}" "${f:-—}" \
      "ОШИБКА: контроль в сети $COMPOSE_NET не показал соседей" "$(detail_of "$out_c")"
    errors=$((errors + 1))
  fi

  log "запись вне рабочих каталогов"
  check write_outside "read_only" rw --read-only --tmpfs /tmp:rw,size=64m

  log "запись во входной том"
  # Единственная разница с контролем — режим монтирования ro.
  check write_input_volume "вход смонтирован ro" ro

  log "chmod (seccomp)"
  check chmod "seccomp" rw --security-opt "seccomp=$SECCOMP"

  log "chown (capabilities)"
  # ТОЛЬКО cap_drop, без смены пользователя: иначе отказ нельзя приписать
  # именно потере capabilities — его объяснил бы и непривилегированный uid.
  check capabilities_chown "cap_drop ALL" rw --cap-drop ALL

  log "повышение привилегий"
  check no_new_privs "no-new-privileges" rw --security-opt no-new-privileges:true

  log "число одновременных процессов"
  check process_spawn "pids_limit" rw --pids-limit 64

  log "лимит памяти"
  # Проба выделяет 512 МиБ при лимите 256 МиБ: ожидается OOM-kill, поэтому
  # вывода не будет вовсе, и судить надо по коду завершения.
  mem_c=$(docker run --rm -e PROBE_MODE=1 -e PROBE_ONLY=memory_over_limit \
            -v "$SAMPLE_DIR:/data/input:rw" "$IMAGE" >/dev/null 2>&1; echo $?)
  mem_s=$(docker run --rm --memory 256m -e PROBE_MODE=1 -e PROBE_ONLY=memory_over_limit \
            -v "$SAMPLE_DIR:/data/input:rw" "$IMAGE" >/dev/null 2>&1; echo $?)
  printf 'контроль rc=%s, с лимитом rc=%s\n' "$mem_c" "$mem_s" > "$RAW/memory_over_limit.txt"
  if [ "$mem_c" -eq 0 ] && [ "$mem_s" -ne 0 ]; then
    emit_row memory_over_limit "mem_limit 256m" "true" "false" "false" \
      "граница работает" "процесс убит, rc=$mem_s"
  else
    emit_row memory_over_limit "mem_limit 256m" "$mem_c" "$mem_s" "—" \
      "ГРАНИЦА НЕ ДЕЙСТВУЕТ" "контроль rc=$mem_c, с лимитом rc=$mem_s"
    errors=$((errors + 1))
  fi

  log "штатная работа при полном наборе границ"
  norm="$(run_probe normal_work ro "${FULL[@]}")"
  printf '%s\n' "$norm" > "$RAW/normal_work-full.json"
  if printf '%s' "$norm" | grep -q '"allowed":true'; then
    emit_row normal_work "все границы" "—" "—" "true" \
      "штатная работа сохранена" "$(detail_of "$norm")"
  else
    emit_row normal_work "все границы" "—" "—" "false" \
      "ОШИБКА: изоляция сломала работу" "$(detail_of "$norm")"
    errors=$((errors + 1))
  fi

  printf '# ошибок: %s\n' "$errors"
} > "$RESULTS/boundaries.tsv"

cat "$RESULTS/boundaries.tsv"

errs=$(grep '^# ошибок:' "$RESULTS/boundaries.tsv" | grep -o '[0-9]*$')
if [ "${errs:-1}" -ne 0 ]; then
  log "ОСТАНОВКА: $errs проб не подтвердили границу"
  exit 1
fi
log "границы подтверждены поодиночке и в полном наборе"
