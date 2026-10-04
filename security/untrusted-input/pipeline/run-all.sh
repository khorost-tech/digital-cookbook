#!/usr/bin/env bash
# Полный цикл стенда: сборка -> запуск -> границы -> сценарий падения -> остановка.
# Один запуск = один снимок: артефакты помечаются общим RUN_ID, а маркер COMPLETE
# появляется только после успешного прохождения всех шагов.

set -uo pipefail
cd "$(dirname "$0")" || exit 1

RESULTS=./results
RUN_ID="$(date -u '+%Y%m%dT%H%M%SZ')"

log() { printf '[run-all] %s\n' "$*" >&2; }

log "RUN_ID=$RUN_ID"

log "0. очистка прошлых результатов"
rm -rf "$RESULTS"
mkdir -p "$RESULTS"
echo "$RUN_ID" > "$RESULTS/RUN_ID"

log "1. сборка образов"
docker compose build >/dev/null 2>&1 || { log "сборка не удалась"; exit 1; }

log "1.5. модульные тесты файлового канала"
# Часть снимка, а не отдельный ритуал: FIFO, символические ссылки и подмену
# объекта проверками на живом конвейере не воспроизвести.
if docker run --rm -v "$(pwd)/supervisor:/src" -w /src -e GOTOOLCHAIN=local \
     golang:1.26.5-alpine sh -c 'go vet ./... && go test -count=1 -timeout 120s ./...' \
     > "$RESULTS/unit-tests.txt" 2>&1; then
  tail -1 "$RESULTS/unit-tests.txt" | sed 's/^/[run-all] /' >&2
else
  log "ОСТАНОВКА: модульные тесты не прошли, см. $RESULTS/unit-tests.txt"
  exit 1
fi

log "2. запуск конвейера"
docker compose up -d >/dev/null 2>&1 || { log "запуск не удался"; exit 1; }
sleep 6

# Версии образов — без них числа и поведение невоспроизводимы.
{
  echo "# Окружение прогона"
  echo "дата: $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo "docker: $(docker --version)"
  echo
  echo "# Образы"
  docker compose images 2>/dev/null | tail -n +2 | sed 's/^/  /'
  echo
  echo "# Роли и границы (из docker-compose.yml)"
  echo "  api        принимает байты, не открывает их; сети pipeline + edge"
  echo "  supervisor держит очередь, не открывает файлы; сеть pipeline,"
  echo "             read_only, cap_drop ALL, no-new-privileges, user 65534,"
  echo "             pids_limit 64, mem_limit 128m"
  echo "  parser     разбирает содержимое (ffprobe); network_mode: none,"
  echo "             read_only, cap_drop ALL, no-new-privileges, user 65534,"
  echo "             pids_limit 64, mem_limit 256m, cpus 1.0,"
  echo "             seccomp=seccomp-parser.json, вход смонтирован только на чтение"
  echo
  echo "# Версия ffmpeg в парсере (тот самый разбирающий код)"
  docker compose run --rm --entrypoint ffprobe parser -version 2>/dev/null | head -1 | sed 's/^/  /'
  echo "  Версия взята из базового образа и НЕ является рекомендацией."
  echo "  Стенд показывает, что даёт изоляция, а не что даёт обновление."
  echo "  В рабочем сервисе нужны оба: и свежая версия, и границы вокруг неё."
} > "$RESULTS/environment.txt"

log "3. проверка границ изоляции"
if ! ./check-boundaries.sh > "$RESULTS/boundaries.log" 2>&1; then
  log "ОСТАНОВКА: границы не подтверждены, см. $RESULTS/boundaries.log"
  docker compose down >/dev/null 2>&1
  exit 1
fi

log "4. сценарии аварий (парсер и супервизор)"
if ! ./check-failure.sh > "$RESULTS/failure.log" 2>&1; then
  log "ОСТАНОВКА: конвейер не пережил аварию, см. $RESULTS/failure.log"
  docker compose down >/dev/null 2>&1
  exit 1
fi

log "5. остановка конвейера"
docker compose down >/dev/null 2>&1

# Метка прогона во всех артефактах: смешать файлы разных запусков не выйдет.
for f in "$RESULTS"/*.tsv "$RESULTS"/*.txt "$RESULTS"/*.log "$RESULTS"/raw/*; do
  [ -f "$f" ] || continue
  case "$(basename "$f")" in RUN_ID|COMPLETE) continue ;; esac
  printf '# run_id=%s\n' "$RUN_ID" >> "$f"
done

{
  printf 'run_id=%s\n' "$RUN_ID"
  printf 'завершён: %s\n' "$(date -u '+%Y-%m-%d %H:%M UTC')"
  printf 'все шаги пройдены: сборка, запуск, границы, сценарии аварий\n'
} > "$RESULTS/COMPLETE"

log "готово. RUN_ID=$RUN_ID, маркер: $RESULTS/COMPLETE"
