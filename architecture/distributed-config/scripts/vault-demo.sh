#!/usr/bin/env bash
set -euo pipefail
# Демо Vault: динамические credentials к PostgreSQL.
# Vault сам создаёт временную учётку в БД по запросу и отзовёт её по TTL.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DC="docker compose -f $ROOT_DIR/docker-compose.yml"
# Все vault-команды выполняем внутри контейнера с dev-токеном.
V() { $DC exec -T -e VAULT_ADDR=http://127.0.0.1:8200 -e VAULT_TOKEN=root vault vault "$@"; }

echo "── Vault: настройка database secrets engine ─────────────────"
V secrets enable -path=database database 2>/dev/null || echo "(database engine уже включён)"

V write database/config/app-db \
  plugin_name=postgresql-database-plugin \
  allowed_roles="app" \
  connection_url="postgresql://{{username}}:{{password}}@postgres:5432/app?sslmode=disable" \
  username="postgres" \
  password="postgres"

V write database/roles/app \
  db_name=app-db \
  default_ttl="1h" \
  max_ttl="24h" \
  creation_statements="CREATE ROLE \"{{name}}\" WITH LOGIN PASSWORD '{{password}}' VALID UNTIL '{{expiration}}'; GRANT SELECT ON ALL TABLES IN SCHEMA public TO \"{{name}}\";"

echo ""
echo "── Vault: выдача ВРЕМЕННЫХ credentials к PostgreSQL ──────────"
V read database/creds/app

echo ""
echo "vault-demo: ок (Vault создал временную учётку в PostgreSQL с TTL=1h)."
