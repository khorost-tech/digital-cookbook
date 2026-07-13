#!/usr/bin/env bash
set -euo pipefail
# Демонстрация ограничения привилегированных портов (<1024) в rootless.
# В rootful публикация на :80 работает из коробки. В rootless — нет,
# пока не понижен net.ipv4.ip_unprivileged_port_start.
#
# Использование:
#   bash scripts/port-80-demo.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

ts() { date '+%H:%M:%S'; }

cleanup() {
  docker rm -f rootless-port80-demo >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "[$(ts)] Сборка образа probe..."
docker compose -f "$ROOT_DIR/docker-compose.yml" build probe >/dev/null

echo "[$(ts)] Пробуем опубликовать probe на привилегированном порту :80..."
if docker run -d --rm --name rootless-port80-demo -p 80:8080 \
     "$(docker compose -f "$ROOT_DIR/docker-compose.yml" config --images | head -1)" \
     >/dev/null 2>err.log; then
  echo "УСПЕХ: порт :80 опубликован."
  echo "→ Скорее всего демон rootful, либо ip_unprivileged_port_start уже понижен."
  docker rm -f rootless-port80-demo >/dev/null 2>&1 || true
else
  echo "ОТКАЗ: не удалось опубликовать :80."
  echo "→ Типично для rootless. Лечение:"
  echo "    sudo sysctl net.ipv4.ip_unprivileged_port_start=80"
  echo "  Подробности ошибки:"
  sed 's/^/    /' err.log 2>/dev/null || true
fi
rm -f err.log 2>/dev/null || true
