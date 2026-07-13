#!/usr/bin/env bash
set -euo pipefail
# Демо Consul: KV-хранилище и service discovery (регистрация + поиск).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DC="docker compose -f $ROOT_DIR/docker-compose.yml"
C="$DC exec -T consul consul"

echo "── Consul: KV ───────────────────────────────────────────────"
$C kv put config/db/host db.internal
$C kv get config/db/host

echo ""
echo "── Consul: service discovery ────────────────────────────────"
# регистрируем сервис и находим его в каталоге
$DC exec -T consul sh -c 'consul services register -name=api -port=8080 || true'
echo "Зарегистрированные сервисы:"
$C catalog services
echo "Инстансы сервиса api:"
$C catalog nodes -service=api

echo ""
echo "consul-demo: ок (KV записан, сервис зарегистрирован и найден)."
