#!/usr/bin/env bash
# Проверяет, что оба плеча Postgres реально доступны: TCP и unix-сокет.
# Скрипт обязан падать на первой же неудаче — «зелёный» вывод без проверок
# ничего не доказывает.
set -euo pipefail

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

echo "== ждём Postgres по TCP =="
for i in $(seq 1 60); do
  if docker compose exec -T bench pg_isready -h postgres -U bench >/dev/null 2>&1; then break; fi
  [ "$i" = "60" ] && fail "Postgres не поднялся по TCP"
done

echo "== ждём Postgres по unix-сокету =="
for i in $(seq 1 60); do
  if docker compose exec -T bench pg_isready -h /var/run/postgresql -U bench >/dev/null 2>&1; then break; fi
  [ "$i" = "60" ] && fail "сокет /var/run/postgresql недоступен из bench"
done

echo "== проверяем, что это ОДИН И ТОТ ЖЕ сервер =="
tcp_id=$(docker compose exec -T bench psql "postgres://bench:bench@postgres:5432/bench" -tAc "select system_identifier from pg_control_system()")
uds_id=$(docker compose exec -T bench psql "postgres:///bench?host=/var/run/postgresql&user=bench&password=bench" -tAc "select system_identifier from pg_control_system()")
[ -n "$tcp_id" ] || fail "пустой system_identifier по TCP"
[ "$tcp_id" = "$uds_id" ] || fail "TCP и сокет ведут в РАЗНЫЕ серверы ($tcp_id vs $uds_id) — сравнение было бы бессмысленным"

echo "SMOKE OK: один сервер, два транспорта; system_identifier=$tcp_id"
