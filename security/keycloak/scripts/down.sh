#!/usr/bin/env bash
#
# down.sh — остановить dev-стенд и удалить его состояние.
# `docker compose down -v` убирает контейнеры, сеть и volume `pgdata` (БД Keycloak),
# поэтому следующий `up.sh` поднимает стенд с нуля и заново импортирует realm demo
# из realm/demo-realm.json (демо-пользователи/клиенты восстанавливаются из файла).
#
# Флаг -v передан намеренно: realm воспроизводим из экспорта, а чистая БД гарантирует
# идемпотентный импорт (--import-realm применяется только к пустой БД).
#
# Требования: docker (+ compose).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

echo "[down] docker compose down -v (контейнеры + сеть + volume pgdata)…" >&2
# --profile bench, чтобы одноразовый сервис-нагрузчик тоже попал под уборку, если запускался.
docker compose --profile bench down -v --remove-orphans

echo "[down] dev-стенд остановлен и очищен." >&2
echo "[down] production-топология (docker-compose.prod.yml) тушится отдельно:" >&2
echo "         docker compose -f docker-compose.prod.yml down -v" >&2
