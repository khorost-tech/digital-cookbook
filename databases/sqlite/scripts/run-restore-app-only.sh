#!/usr/bin/env bash
# Второй сценарий отказа — контраст к run-restore.sh.
#
# run-restore.sh убивает bench И litestream ОДНОЙ командой docker kill:
# это модель ОДНОВРЕМЕННОЙ ГИБЕЛИ ПИСАТЕЛЯ И РЕПЛИКАТОРА ПРИ СОХРАННОМ
# ХРАНИЛИЩЕ (реплика умирает вместе с источником, но MinIO жив) — не гибель
# хоста целиком: там теряется всё, что не успело уехать за интервал
# синхронизации.
#
# Здесь умирает ТОЛЬКО приложение (bench), litestream остаётся жив и
# продолжает работать как ни в чём не бывало: у него есть ещё >= 1 тик
# sync-interval, чтобы дожать в MinIO хвост WAL, накопившийся к моменту
# смерти bench. Это модель падения контейнера приложения (OOM, паника,
# ручной restart) при штатно работающей инфраструктуре репликации.
# Ожидание: потеря около нуля — реплика догоняет сама, без нашего участия.
set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."
OUT="${OUT:-fixtures/restore-app-only.txt}"
{
  echo "== создаём бакет (idempotent) =="
  docker compose exec -T minio mc alias set local http://localhost:9000 minioadmin minioadmin
  docker compose exec -T minio mc mb -p local/sqlite-backup 2>&1 || true

  echo "== запускаем непрерывную запись в фоне =="
  docker compose exec -d bench bench -arm sqlite -op insert -n 5000000 -sync NORMAL

  echo "== ждём, пока запись реально пойдёт (счётчик растёт) =="
  prev=-1
  for i in $(seq 1 60); do
    cur=$(docker compose exec -T bench sqlite3 /data/bench.db "select count(*) from events" 2>/dev/null || echo -1)
    if [ "$cur" != "-1" ] && [ "$cur" != "$prev" ] && [ "$prev" != "-1" ]; then
      break
    fi
    prev="$cur"
    sleep 1
  done

  echo "== пишем ещё 15 секунд под нагрузкой =="
  sleep 15

  echo "== убиваем ТОЛЬКО bench, litestream остаётся жив =="
  docker compose kill -s SIGKILL bench

  echo "== даём litestream досинхронизировать хвост WAL (2 тика sync-interval: 1s) =="
  sleep 2

  before=$(docker compose exec -T litestream sqlite3 /data/bench.db "select count(*) from events")
  echo "строк в базе после того, как litestream (живой) дотянул хвост: $before"

  echo "== теперь останавливаем и litestream, чтобы дальше он файл не трогал =="
  docker compose kill -s SIGKILL litestream

  echo "== стираем локальный том =="
  docker compose run --rm --no-deps --entrypoint sh -T litestream -c 'rm -f /data/bench.db /data/bench.db-wal /data/bench.db-shm'

  echo "== восстанавливаем из MinIO =="
  docker compose run --rm --no-deps -T litestream restore -config /etc/litestream.yml /data/bench.db

  after=$(docker compose run --rm --no-deps --entrypoint sqlite3 -T litestream /data/bench.db "select count(*) from events" || echo "0")
  echo "строк после восстановления: $after"
  echo "потеряно: $((before - after))"

  echo "== поднимаем стенд обратно (bench + litestream replicate) =="
  docker compose up -d
} | tee "$OUT"
