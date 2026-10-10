#!/usr/bin/env bash
# Проверяем не обещание «данные в безопасности», а факт: сколько коммитов
# пережило ОДНОВРЕМЕННОЕ УБИЙСТВО ПИСАТЕЛЯ И РЕПЛИКАТОРА ПРИ СОХРАННОМ
# ХРАНИЛИЩЕ — это НЕ гибель хоста целиком: MinIO в этом Compose-проекте
# живёт на том же хосте, и при реальной гибели хоста погиб бы вместе с
# данными — восстанавливать было бы уже неоткуда. Сценарий моделирует отказ
# приложения или узла ПРИ УДАЛЁННОМ объектном хранилище (S3 — внешний
# сервис) — топология, типичная на практике. Пишем под непрерывной
# litestream-репликацией (sync-interval: 1s), затем убиваем bench SIGKILL
# прямо посреди записи И СРАЗУ ЖЕ следом убиваем сам litestream — иначе он
# успевает досинхронизировать «хвост» WAL уже после смерти bench (запись
# остановилась, а фоновый тик репликации ещё жив) и результат врёт нулём.
# После обоих SIGKILL читаем локальный файл как есть (подлинное состояние на
# момент отказа), стираем его, восстанавливаем из MinIO и сравниваем счётчики.
set -euo pipefail
export MSYS_NO_PATHCONV=1
cd "$(dirname "$0")/.."
OUT="${OUT:-fixtures/restore.txt}"
{
  echo "== создаём бакет =="
  docker compose exec -T minio mc alias set local http://localhost:9000 minioadmin minioadmin
  docker compose exec -T minio mc mb -p local/sqlite-backup

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

  echo "== одновременное убийство писателя и репликатора ОДНОЙ командой docker kill (хранилище MinIO сохранно) =="
  # Два отдельных "docker compose kill" — это два отдельных вызова CLI, между
  # которыми litestream успевает сделать ещё один тик (1s) и дожать хвост WAL:
  # тогда потеря всегда получается 0, и это ничего не проверяет. Один вызов
  # `docker kill id1 id2` убивает оба процесса одной командой — но это одна
  # команда, а не гарантия атомарности: демон Docker всё равно рассылает два
  # отдельных SIGKILL, и окно между ними теоретически возможно, просто
  # значительно короче, чем при двух отдельных вызовах CLI.
  bench_cid=$(docker compose ps -q bench)
  litestream_cid=$(docker compose ps -q litestream)
  docker kill -s SIGKILL "$bench_cid" "$litestream_cid"

  # Оба процесса мертвы, локальный файл дальше никто не трогает. Читаем его
  # одноразовым контейнером litestream (тем же volume), не поднимая replicate.
  before=$(docker compose run --rm --no-deps --entrypoint sqlite3 -T litestream /data/bench.db "select count(*) from events")
  echo "строк в базе до отказа (заморожено на момент SIGKILL): $before"

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
