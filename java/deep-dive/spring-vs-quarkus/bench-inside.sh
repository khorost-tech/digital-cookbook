#!/usr/bin/env bash
# Замер ВНУТРИ контейнера — без публикации портов на хост (-p) и без обращения
# к сервису с хоста. Так измеряется чистый startup процесса, не искажённый
# port-forwarder'ом Docker Desktop на Windows (тот добавляет случайные 20–80 с
# на первый коннект — артефакт хоста, а не JVM/native, воспроизведён и исключён
# этим переносом замера внутрь).
#
# Аргумент $1 — команда запуска сервиса (строкой, исполняется через eval).
# Печатает одну строку:  startup_ms=<...> peak_rss_kb=<...>
#
# startup  — от запуска процесса до первого HTTP 200 на GET /health,
#            опрос через bash /dev/tcp (curl есть не во всех рантайм-образах).
# peak RSS — VmHWM процесса сервиса из /proc/$PID/status (историчный пик
#            резидентной памяти — та же метрика, что в ../concurrency),
#            снимается после короткой паузы на устаканивание.
set -uo pipefail

START_CMD="$1"

t0=$(date +%s%N)
eval "$START_CMD & echo \$! > /tmp/svc.pid"
PID=$(cat /tmp/svc.pid)

ok=0
for _ in $(seq 1 4000); do
  if exec 3<>/dev/tcp/127.0.0.1/8080 2>/dev/null; then
    printf 'GET /health HTTP/1.0\r\n\r\n' >&3
    resp=$(head -1 <&3 2>/dev/null || true)
    exec 3<&- 3>&- 2>/dev/null || true
    case "$resp" in
      *200*) ok=1; break;;
    esac
  fi
  sleep 0.002
done
t1=$(date +%s%N)

if [ "$ok" != "1" ]; then
  echo "startup_ms=FAIL peak_rss_kb=0"
  kill "$PID" 2>/dev/null || true
  exit 1
fi

startup_ms=$(( (t1 - t0) / 1000000 ))

# Пик RSS формируется в первые доли секунды жизни JVM (загрузка классов, JIT).
# Дать процессу устаканиться и снять VmHWM (историчный максимум) — одного
# health-запроса уже достаточно, чтобы прогреть путь до HTTP-хендлера.
sleep 0.5
rss_kb=$(awk '/VmHWM/{print $2}' "/proc/$PID/status" 2>/dev/null || echo 0)

echo "startup_ms=${startup_ms} peak_rss_kb=${rss_kb:-0}"
kill "$PID" 2>/dev/null || true
