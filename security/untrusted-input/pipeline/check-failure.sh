#!/usr/bin/env bash
# Что происходит с конвейером, когда падает недоверенная зона — и когда падает
# координатор.
#
# Ради этого и городится разделение: сбой при разборе недоверенного файла должен
# оставаться локальной неприятностью. Проверяем:
#   1. штатную обработку (контроль — без него остальные шаги ничего не значат);
#   2. приём файла, ПОКА ПАРСЕР НЕ РАБОТАЕТ: задача принята и не потеряна;
#   3. аварию ПАРСЕРА: API и супервизор не задеты, восстановление автоматическое;
#   4. падение СУПЕРВИЗОРА посреди задачи: задача возвращается из processing
#      и доводится до результата после перезапуска.
#
# Шаги 2 и 3 разделены намеренно. Раньше загрузка при «мёртвом» парсере шла
# сразу после аварии, но ожидание перезапуска происходило внутри той же
# функции — к моменту загрузки парсер был уже жив. Сценарий выглядел
# воспроизведённым, не будучи таковым.
#
# Падение вызывается сигналом главному процессу ИЗНУТРИ контейнера, а не «злым»
# файлом: нужна авария, а не эксплуатация уязвимости. Внешний `docker kill` для
# этого не годится — Docker считает его намеренной остановкой и не применяет
# политику перезапуска, а проверяется именно автоматическое восстановление.
#
# Проверки строгие: сверяются коды HTTP, статус в ответе и совпадение task_id.
# Раньше достаточно было «ответ не pending» — под это подходила и ошибка.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

RESULTS=./results
mkdir -p "$RESULTS"
OUT="$RESULTS/failure-mode.txt"
TMP=/tmp/ui-pipe-failure
mkdir -p "$TMP"

API=http://127.0.0.1:8080
SAMPLE_DIR=/tmp/ui-pipe-sample
errors=0

log()  { printf '[failure] %s\n' "$*" >&2; }
fail() { errors=$((errors + 1)); printf '   ОТКАЗ: %s\n' "$*"; }

# Код HTTP отдельно от тела: «ответ пришёл» и «ответ успешный» — разные вещи.
http_code() { curl -sS --max-time 10 -o "$TMP/body" -w '%{http_code}' "$@" 2>/dev/null; }

upload() {
  http_code -X POST --data-binary "@$SAMPLE_DIR/sample.avi" "$API/upload"
}

task_id_of() { sed 's/.*"task_id":"\([^"]*\)".*/\1/' "$TMP/body"; }

# Ждём окончательный ответ: 202 — задача ещё в работе, 200 — результат готов.
# Печатает код HTTP, тело остаётся в $TMP/body.
wait_result() {
  local id="$1" code deadline=$((SECONDS + ${2:-45}))
  while [ "$SECONDS" -lt "$deadline" ]; do
    code="$(http_code "$API/result?id=$id")"
    [ "$code" = "202" ] || { printf '%s' "$code"; return 0; }
    sleep 1
  done
  printf '408'
}

# Успех — это код 200, статус ok И тот самый идентификатор.
check_ok() {
  local what="$1" id="$2" code="$3" body
  body="$(tr -d '\n' < "$TMP/body")"
  printf '   %s: HTTP %s %s\n' "$what" "$code" "$body"
  [ "$code" = "200" ] || { fail "$what: ожидался HTTP 200"; return 1; }
  printf '%s' "$body" | grep -q '"status":"ok"' || { fail "$what: статус не ok"; return 1; }
  printf '%s' "$body" | grep -q "\"task_id\":\"$id\"" || { fail "$what: чужой task_id"; return 1; }
  return 0
}

restarts_of() { docker inspect -f '{{.RestartCount}}' "$1" 2>/dev/null || echo '?'; }

# Аварийное завершение главного процесса контейнера изнутри.
# Возвращает строку «было N перезапусков -> стало M»: это и есть доказательство
# аварии. Мгновенный снимок состояния для этого не годится — к моменту проверки
# контейнер уже поднят политикой restart и показывает running с кодом 0,
# то есть выглядит так, будто ничего не случилось.
crash() {
  local name="$1" before after deadline
  before="$(restarts_of "$name")"
  docker exec "$name" kill -SEGV 1 >/dev/null 2>&1 || \
  docker exec "$name" sh -c 'kill -9 1' >/dev/null 2>&1
  deadline=$((SECONDS + 30))
  while [ "$SECONDS" -lt "$deadline" ]; do
    after="$(restarts_of "$name")"
    [ "$after" != "$before" ] && break
    sleep 1
  done
  printf 'перезапусков было %s, стало %s' "$before" "$after"
  # Признак кладём в файл: crash вызывается в подстановке команд, и счётчик
  # ошибок, увеличенный в подоболочке, до основного процесса бы не дошёл.
  if [ "$after" != "$before" ]; then echo yes > "$TMP/crashed"; else echo no > "$TMP/crashed"; fi
}

crash_confirmed() {
  [ "$(cat "$TMP/crashed" 2>/dev/null)" = "yes" ] || fail "$1 не перезапускался — авария не воспроизведена"
}

wait_running() {
  local name="$1" deadline=$((SECONDS + 40))
  while [ "$SECONDS" -lt "$deadline" ]; do
    [ "$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null)" = "running" ] && return 0
    sleep 1
  done
  return 1
}

[ -f "$SAMPLE_DIR/sample.avi" ] || { log "нет образца — сначала ./check-boundaries.sh"; exit 1; }

{
  echo "# Поведение конвейера при авариях"
  echo

  # --- 1. Контроль -----------------------------------------------------------
  log "1. штатная обработка (контроль)"
  echo "1. Контроль — конвейер исправен:"
  code="$(upload)"
  id1="$(task_id_of)"
  if [ "$code" != "200" ]; then
    fail "приём файла вернул HTTP $code"
  else
    rcode="$(wait_result "$id1")"
    check_ok "результат" "$id1" "$rcode"
  fi

  # --- 2. Приём при неработающем парсере -------------------------------------
  # Отдельный сценарий, а не побочный эффект аварии. Раньше загрузка шла после
  # crash(), но crash() возвращает управление, лишь дождавшись перезапуска, —
  # к моменту загрузки парсер был уже жив, и утверждение «файл принят при
  # мёртвом парсере» артефактом не подтверждалось.
  #
  # Поэтому здесь парсер останавливается явно: docker stop не поднимает
  # контейнер обратно (restart: unless-stopped), и состояние «не работает»
  # держится ровно столько, сколько нужно проверке.
  log "2. приём файла, пока парсер не работает"
  echo
  echo "2. Парсер остановлен, приём продолжается:"
  docker stop ui-pipe-parser >/dev/null 2>&1
  pstate="$(docker inspect -f '{{.State.Status}}' ui-pipe-parser 2>/dev/null)"
  if [ "$pstate" = "running" ]; then
    fail "парсер не остановился — сценарий не воспроизведён"
  else
    echo "   состояние парсера: $pstate"
  fi

  hcode="$(http_code "$API/health")"
  [ "$hcode" = "200" ] && echo "   API: HTTP 200 — работает без парсера" \
                       || fail "API ответил HTTP $hcode"

  code="$(upload)"
  id2="$(task_id_of)"
  [ "$code" = "200" ] || fail "приём при остановленном парсере вернул HTTP $code"
  echo "   загрузка при остановленном парсере: HTTP 200, задача $id2"

  # Задача обязана лежать в очереди: либо ещё не взята, либо взята и ждёт
  # ответа в processing. Если её нет нигде — она потеряна.
  sleep 2
  qn="$(docker exec ui-pipe-redis redis-cli LRANGE pipeline:tasks 0 -1 2>/dev/null | grep -c "$id2")"
  pn="$(docker exec ui-pipe-redis redis-cli LRANGE pipeline:processing 0 -1 2>/dev/null | grep -c "$id2")"
  echo "   задача в tasks: $qn, в processing: $pn"
  [ $((qn + pn)) -ge 1 ] || fail "задача $id2 не найдена ни в tasks, ни в processing"

  docker start ui-pipe-parser >/dev/null 2>&1
  if wait_running ui-pipe-parser; then
    echo "   парсер запущен обратно"
    rcode="$(wait_result "$id2" 60)"
    check_ok "задача, принятая без парсера" "$id2" "$rcode"
  else
    fail "парсер не запустился"
  fi

  # --- 3. Авария парсера и автоматическое восстановление ---------------------
  log "3. авария в парсере — в недоверенной зоне"
  echo
  echo "3. Аварийно завершён ПАРСЕР (недоверенная зона):"
  echo "   парсер: $(crash ui-pipe-parser)"
  crash_confirmed ui-pipe-parser

  hcode="$(http_code "$API/health")"
  [ "$hcode" = "200" ] && echo "   API: HTTP 200 — сбой парсера его не затронул" \
                       || fail "API ответил HTTP $hcode"
  sup="$(docker inspect -f '{{.State.Status}}' ui-pipe-supervisor 2>/dev/null)"
  [ "$sup" = "running" ] && echo "   супервизор: running — сбой не перешёл через границу контейнера" \
                         || fail "супервизор в состоянии '$sup'"

  if wait_running ui-pipe-parser; then
    echo "   парсер поднят политикой restart"
    code="$(upload)"
    id3="$(task_id_of)"
    [ "$code" = "200" ] || fail "приём после аварии вернул HTTP $code"
    rcode="$(wait_result "$id3" 60)"
    check_ok "обработка после аварии" "$id3" "$rcode"
  else
    fail "парсер не перезапустился"
  fi

  # --- 4. Падение супервизора посреди задачи ---------------------------------
  log "4. авария в супервизоре, пока задача взята в работу"
  echo
  echo "4. Аварийно завершён СУПЕРВИЗОР с задачей в работе:"
  # Парсер намеренно останавливаем, чтобы задача гарантированно застряла
  # в processing к моменту падения супервизора.
  docker stop ui-pipe-parser >/dev/null 2>&1
  code="$(upload)"
  id4="$(task_id_of)"
  [ "$code" = "200" ] || fail "приём вернул HTTP $code"
  sleep 3

  inproc="$(docker exec ui-pipe-redis redis-cli LRANGE pipeline:processing 0 -1 2>/dev/null | grep -c "$id4")"
  echo "   задача $id4 в списке processing: $inproc"
  [ "$inproc" -ge 1 ] || fail "задача не попала в processing — сценарий не воспроизведён"

  echo "   супервизор: $(crash ui-pipe-supervisor)"
  crash_confirmed ui-pipe-supervisor

  hcode="$(http_code "$API/health")"
  [ "$hcode" = "200" ] && echo "   API: HTTP 200 — падение координатора его не затронуло" \
                       || fail "API ответил HTTP $hcode"

  docker start ui-pipe-parser >/dev/null 2>&1
  if wait_running ui-pipe-supervisor && wait_running ui-pipe-parser; then
    echo "   супервизор и парсер снова работают"
    # Возврат из processing происходит по истечении отметки «взята в работу»
    # (STALE_AFTER) — задача не подтверждена, значит не потеряна.
    rcode="$(wait_result "$id4" 90)"
    check_ok "задача, пережившая падение координатора" "$id4" "$rcode"
  else
    fail "сервисы не восстановились"
  fi

  # --- Состояние очередей после всех аварий ----------------------------------
  echo
  echo "5. Очереди после всех сценариев:"
  # Нули обязательны во ВСЕХ трёх списках, а не только в poison. Отчёт
  # использует пустые очереди как доказательство, что ничего не потерялось
  # и не зависло, — значит непустой tasks или processing должен останавливать
  # прогон, а не просто печататься строкой.
  for k in pipeline:tasks pipeline:processing pipeline:poison; do
    n="$(docker exec ui-pipe-redis redis-cli LLEN "$k" 2>/dev/null | tr -d '\r')"
    printf '   %-22s %s\n' "$k" "${n:-?}"
    case "${n:-нет ответа}" in
      0) ;;
      *) fail "$k не пуст: $n — задачи зависли или ушли в отбраковку" ;;
    esac
  done

  echo
  echo "Вывод: аварии остались внутри своих контейнеров. API не пострадал ни разу,"
  echo "принятые задачи доведены до результата после перезапуска."
  echo "Оговорка: доставка здесь НЕ МЕНЬШЕ ОДНОГО РАЗА. Задача, не подтверждённая"
  echo "до падения, будет обработана повторно — обработчик обязан быть идемпотентным."
  printf '# ошибок: %s\n' "$errors"
} | tee "$OUT"

errs="$(grep '^# ошибок:' "$OUT" | grep -o '[0-9]*$')"
if [ -z "$errs" ]; then
  # Итоговой строки нет — значит скрипт оборвался, не дойдя до конца.
  # Отчёт без неё недостоверен целиком, а не «почти полон».
  log "ОСТАНОВКА: отчёт оборван, итоговая строка отсутствует"
  exit 1
fi
if [ "$errs" -ne 0 ]; then
  log "ОСТАНОВКА: $errs проверок не прошли"
  exit 1
fi
log "сценарии аварий пройдены"
