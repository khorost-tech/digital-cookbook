#!/usr/bin/env bash
set -euo pipefail
# Сквозная демонстрация (неинтерактивно):
#   1. docker compose up -d (etcd, consul, vault, postgres)
#   2. Ожидание готовности
#   3. etcd-demo / consul-demo / vault-demo
#   4. docker compose down -v (trap EXIT — всегда)
#
# Использование:
#   bash scripts/smoke.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DC="docker compose -f $ROOT_DIR/docker-compose.yml"

ts() { date '+%H:%M:%S'; }

cleanup() {
  echo "" >&2
  echo "[$(ts)] Trap EXIT — docker compose down -v..." >&2
  $DC down -v 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "[$(ts)] ОШИБКА: $1" >&2; exit 1; }

echo "============================================================"
echo " Demo: распределённая конфигурация — etcd, Consul, Vault"
echo " Время старта: $(ts)"
echo "============================================================"

echo "[$(ts)] docker compose up -d..."
$DC up -d || fail "compose up не удался"

echo "[$(ts)] Ожидание готовности сервисов..."
for i in $(seq 1 30); do
  if $DC exec -T etcd etcdctl endpoint health >/dev/null 2>&1 \
     && $DC exec -T consul consul members >/dev/null 2>&1 \
     && $DC exec -T -e VAULT_ADDR=http://127.0.0.1:8200 vault vault status >/dev/null 2>&1 \
     && $DC exec -T postgres pg_isready -U postgres -d app >/dev/null 2>&1; then
    break
  fi
  [ "$i" -eq 30 ] && fail "сервисы не поднялись за 30 попыток"
  sleep 2
done

echo ""
bash "$SCRIPT_DIR/etcd-demo.sh"   || fail "etcd-demo"
echo ""
bash "$SCRIPT_DIR/consul-demo.sh" || fail "consul-demo"
echo ""
bash "$SCRIPT_DIR/vault-demo.sh"  || fail "vault-demo"

echo ""
echo "[$(ts)] Smoke завершён успешно."
