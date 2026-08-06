#!/usr/bin/env bash
#
# run.sh — сравнивает два варианта и печатает медиану с разбросом.
#
# Почему не «просто go test ./...»: пакеты go test запускает ПАРАЛЛЕЛЬНО, и
# тогда наивный вариант соревнуется за демон с шаблонным — оба замера
# оказываются ни о чём, а на слабой машине наивный ещё и падает по таймауту
# ожидания порта. Здесь каждый вариант идёт отдельным вызовом, по одному за
# раз, с уборкой и паузой между прогонами.
#
#   ./run.sh                                    # 20 случаев, 4 повтора
#   STAND_CASES=50 STAND_REPEATS=6 ./run.sh
#
# Число повторов принудительно чётное: порядок вариантов чередуется, и при
# нечётном числе один из них оказался бы первым чаще другого. Порядок здесь
# влияет сильно — это выяснилось замером, а не предположением, см. README.
#
# Скрипт НЕ печатает результат, если окружение непригодно: без go, без docker,
# без доступа к демону или при чужих активных Testcontainers он падает с
# внятной ошибкой. Таблица, полученная из грязного окружения, хуже её
# отсутствия.
#
# Логи упавших прогонов сохраняются в logs/.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

cases="${STAND_CASES:-20}"
repeats="${STAND_REPEATS:-4}"
if [ $((repeats % 2)) -ne 0 ]; then
  repeats=$((repeats + 1))
  echo "run.sh: число повторов округлено до чётного (${repeats}) — порядок вариантов чередуется" >&2
fi

run_id="$(date +%s)-$$-${RANDOM}"
stand_label="tech.khorost.stand=testcontainers-template-db"
run_label="tech.khorost.run=${run_id}"
ryuk_filter="label=org.testcontainers.ryuk=true"

export STAND_CASES="$cases"
export STAND_RUN="$run_id"

logdir="logs/${run_id}"

die() { echo "run.sh: $*" >&2; exit 1; }

# --- Проверки окружения до всякого замера --------------------------------
command -v go >/dev/null 2>&1 || die "нет go в PATH"
command -v docker >/dev/null 2>&1 || die "нет docker в PATH"
docker info >/dev/null 2>&1 || die "docker есть, но демон недоступен (права? не запущен?)"
go env GOMOD >/dev/null 2>&1 || die "go не может прочитать модуль — запускайте из каталога стенда"

# Чужие реаперы Testcontainers. Мы их не трогаем — но и мерить рядом с ними
# нельзя: они означают, что на демоне прямо сейчас идёт чужой прогон, и его
# нагрузка ляжет в наши числа. Раньше скрипт молча ждал их исчезновения и
# падал с «демон не пришёл в себя», хотя демон был жив, а причина была совсем
# другой.
foreign_ryuk=""
if ! foreign_ryuk=$(docker ps -q --filter "$ryuk_filter" 2>&1); then
  die "docker ps не отработал: ${foreign_ryuk}"
fi
if [ -n "$foreign_ryuk" ]; then
  echo "run.sh: на демоне активны чужие Testcontainers (реаперы:" >&2
  docker ps --filter "$ryuk_filter" --format '  {{.ID}}  {{.Image}}  {{.Status}}' >&2 || true
  die "дождитесь их завершения — рядом с чужим прогоном замер бессмысленен. Мы ничего чужого не удаляем."
fi

# --- Свои контейнеры ------------------------------------------------------
# MINE_IDS/MINE_COUNT заполняются функцией, а не пайпом: die внутри пайпа
# завершил бы только дочерний shell, а вызывающий получил бы пустой список и
# честно решил, что контейнеров ноль. Проверено — именно так и происходило.
MINE_IDS=""
MINE_COUNT=0
refresh_mine() {
  local out
  if ! out=$(docker ps -aq --filter "label=${stand_label}" --filter "label=${run_label}" 2>&1); then
    die "docker ps не отработал (демон пропал?): ${out}"
  fi
  MINE_IDS="$out"
  if [ -z "$out" ]; then
    MINE_COUNT=0
  else
    MINE_COUNT=$(printf '%s\n' "$out" | wc -l | tr -d ' ')
  fi
}

cleanup_mine() {
  refresh_mine
  if [ -n "$MINE_IDS" ]; then
    docker rm -f $MINE_IDS >/dev/null 2>&1 || die "не удалось убрать контейнеры запуска ${run_id}"
  fi
  local i
  for i in $(seq 1 30); do
    refresh_mine
    [ "$MINE_COUNT" = "0" ] && break
    [ "$i" = "30" ] && die "свои контейнеры не убрались за 30 с — замер был бы грязным"
    sleep 1
  done
  settle
}

# settle — ждать, пока демон придёт в себя после предыдущего прогона.
#
# Мало убрать контейнеры: после десятков созданий и удалений Docker API ещё
# какое-то время отвечает медленно, и следующий вариант меряет не себя, а
# восстановление после предыдущего. Обнаружено чередованием порядка — пока
# шаблонный вариант всегда шёл первым, он выглядел идеально стабильным.
#
# Ждём двух вещей: чтобы догорели реаперы (наши — чужих здесь уже быть не
# может, preflight их отсёк) и чтобы docker info отвечал быстро.
settle() {
  local i ryuk start end ms
  for i in $(seq 1 60); do
    if ! ryuk=$(docker ps -q --filter "$ryuk_filter" 2>&1); then
      die "docker ps не отработал во время паузы: ${ryuk}"
    fi

    start=$(date +%s%N)
    docker info >/dev/null 2>&1 || die "демон перестал отвечать во время паузы"
    end=$(date +%s%N)
    ms=$(( (end - start) / 1000000 ))

    if [ -z "$ryuk" ] && [ "$ms" -lt "${STAND_SETTLE_MS:-1500}" ]; then
      return 0
    fi
    sleep 1
  done

  if [ -n "$ryuk" ]; then
    die "реаперы прошлого прогона не догорели за 60 с"
  fi
  die "Docker API отвечает дольше ${STAND_SETTLE_MS:-1500} мс уже 60 с — замер был бы грязным"
}

median() {
  printf '%s\n' "$@" | sort -n |
    awk '{v[NR]=$1} END {print (NR%2) ? v[(NR+1)/2] : (v[NR/2]+v[NR/2+1])/2}'
}

# Время возвращается через глобальную LAST_TIME, а не через подстановку
# команды: в подшелле die не завершил бы скрипт, и фатальная ошибка уборки
# была бы принята за обычное падение прогона.
LAST_TIME=""
run_once() {
  local pkg="$1" n="$2" log start end
  cleanup_mine
  log="${logdir}/${pkg}-${n}.log"
  start=$(date +%s.%N)
  if go test "./${pkg}/" -count=1 -v >"$log" 2>&1; then
    end=$(date +%s.%N)
    LAST_TIME=$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.1f", b - a }')
    rm -f "$log"
    return 0
  fi
  return 1
}

# Позиции считаются по НАЗНАЧЕННЫМ запускам, а не по успешным.
#
# Сначала было наоборот, и колонка врала ровно там, где должна была помогать:
# naive запускался первым и вторым по разу, первый запуск упал — и отчёт
# показывал «первым: 0, вторым: 1», пряча связь между позицией и падением.
# Ради этой связи колонка и заводилась.
report() {
  local title="$1" af="$2" as="$3" ff="$4" fs="$5"
  shift 5
  local times=("$@")
  local failed=$((ff + fs))

  if [ ${#times[@]} -eq 0 ]; then
    printf '%-34s ВСЕ %s прогонов упали — логи в %s/\n' "$title" "$repeats" "$logdir"
    printf '%-34s   назначено первым: %s, вторым: %s; упали: первым %s, вторым %s\n' \
      "" "$af" "$as" "$ff" "$fs"
    return 1
  fi

  local med lo hi note=""
  med=$(median "${times[@]}")
  lo=$(printf '%s\n' "${times[@]}" | sort -n | head -1)
  hi=$(printf '%s\n' "${times[@]}" | sort -n | tail -1)
  [ "$failed" -gt 0 ] && note=$(printf '  ПЛЮС %s из %s упали (логи в %s/)' "$failed" "$repeats" "$logdir")
  awk -v l="$title" -v m="$med" -v lo="$lo" -v hi="$hi" -v n="$note" \
    'BEGIN { printf "%-34s медиана %7.1f s   разброс %.1f–%.1f%s\n", l, m, lo, hi, n }'
  if [ "$failed" -gt 0 ]; then
    printf '%-34s   назначено первым: %s, вторым: %s; упали: первым %s, вторым %s\n' \
      "" "$af" "$as" "$ff" "$fs"
  else
    printf '%-34s   назначено первым: %s, вторым: %s; падений нет\n' "" "$af" "$as"
  fi
  return 0
}

mkdir -p "$logdir"

echo "=== окружение ==="
printf 'ядро          %s\n' "$(uname -sr)"
printf 'CPU           %s шт.\n' "$(nproc)"
printf 'память        %s\n' "$(awk '/MemTotal/ {printf "%.1f ГиБ", $2/1048576}' /proc/meminfo 2>/dev/null || echo '?')"
printf 'docker        клиент %s, сервер %s\n' \
  "$(docker version --format '{{.Client.Version}}')" \
  "$(docker version --format '{{.Server.Version}}')"
printf 'go            %s\n' "$(go version | awk '{print $3}')"
printf 'образ         закреплён по digest (см. stand/stand.go)\n'
echo

echo "=== параметры ==="
printf 'случаев (изолированных баз)   %s\n' "$cases"
printf 'повторов на вариант           %s (чётное: порядок чередуется поровну)\n' "$repeats"
printf 'метка запуска                 %s\n' "$run_id"
echo

echo "=== замер ==="
t_tmpl=(); t_naive=()
# af/as — назначено первым/вторым, ff/fs — упало первым/вторым.
af_tmpl=0; as_tmpl=0; ff_tmpl=0; fs_tmpl=0
af_naive=0; as_naive=0; ff_naive=0; fs_naive=0

for n in $(seq 1 "$repeats"); do
  if [ $((n % 2)) -eq 1 ]; then order=(tmpl naive); else order=(naive tmpl); fi
  pos=0
  for pkg in "${order[@]}"; do
    pos=$((pos + 1))

    # Позиция засчитывается ДО запуска: иначе упавший прогон исчезал бы из
    # баланса позиций, и связь «падает, когда идёт первым» была бы не видна.
    case "$pkg" in
      tmpl)  [ "$pos" = "1" ] && af_tmpl=$((af_tmpl + 1))  || as_tmpl=$((as_tmpl + 1)) ;;
      naive) [ "$pos" = "1" ] && af_naive=$((af_naive + 1)) || as_naive=$((as_naive + 1)) ;;
    esac

    if run_once "$pkg" "$n"; then
      case "$pkg" in
        tmpl)  t_tmpl+=("$LAST_TIME") ;;
        naive) t_naive+=("$LAST_TIME") ;;
      esac
    else
      case "$pkg" in
        tmpl)  [ "$pos" = "1" ] && ff_tmpl=$((ff_tmpl + 1))  || fs_tmpl=$((fs_tmpl + 1)) ;;
        naive) [ "$pos" = "1" ] && ff_naive=$((ff_naive + 1)) || fs_naive=$((fs_naive + 1)) ;;
      esac
    fi
  done
done

rc=0
report "TEMPLATE (общий контейнер)"  "$af_tmpl"  "$as_tmpl"  "$ff_tmpl"  "$fs_tmpl" \
  ${t_tmpl[@]+"${t_tmpl[@]}"}  || rc=1
report "naive (контейнер на случай)" "$af_naive" "$as_naive" "$ff_naive" "$fs_naive" \
  ${t_naive[@]+"${t_naive[@]}"} || rc=1

cleanup_mine
rmdir "$logdir" 2>/dev/null && rmdir logs 2>/dev/null || true

refresh_mine
echo
printf 'своих контейнеров после уборки: %s\n' "$MINE_COUNT"
exit "$rc"
