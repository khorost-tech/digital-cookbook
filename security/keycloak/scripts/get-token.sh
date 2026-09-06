#!/usr/bin/env bash
#
# get-token.sh — получить access-token realm demo по password grant (OAuth2 Resource
# Owner Password Credentials) через public-клиент `cli` с включённым Direct Access Grants.
# Печатает сырой access-token; с флагом -d/--decode дополнительно показывает декодированный
# payload (iss/aud/exp/azp/realm_access.roles) без проверки подписи.
#
#   scripts/get-token.sh            # bob (роли user+admin)  — по умолчанию
#   scripts/get-token.sh alice      # alice (роль user)
#   scripts/get-token.sh bob -d     # + декодированный payload
#
# Демо-пользователи (demo-only, пароли non-temporary из realm/demo-realm.json):
#   alice / alice-demo-2026   → realm-роль user
#   bob   / bob-demo-2026     → realm-роли user + admin
#
# Password grant — только для demo-скриптов (получить токен из CLI). В реальных приложениях
# фронт использует Authorization Code + PKCE (клиент `frontend`); см. README.
#
# Требования: docker-стенд поднят (scripts/up.sh), curl. Печатает токен в stdout,
# служебные сообщения — в stderr (удобно: TOKEN="$(scripts/get-token.sh bob)").
set -euo pipefail

REALM="${REALM:-demo}"
CLIENT_ID="${CLIENT_ID:-cli}"
TOKEN_URL="${TOKEN_URL:-http://localhost:8080/realms/$REALM/protocol/openid-connect/token}"

USER="bob"
DECODE=0
for arg in "$@"; do
  case "$arg" in
    alice|bob) USER="$arg" ;;
    -d|--decode) DECODE=1 ;;
    *) echo "[get-token] неизвестный аргумент: $arg (ожидается bob|alice, -d)" >&2; exit 2 ;;
  esac
done

case "$USER" in
  alice) PASS="${ALICE_PWD:-alice-demo-2026}" ;;
  bob)   PASS="${BOB_PWD:-bob-demo-2026}" ;;
esac

echo "[get-token] user=$USER client=$CLIENT_ID realm=$REALM (grant=password)" >&2

# --retry: опубликованный порт Docker Desktop на Windows изредка сбрасывает первый
# коннект после простоя — ретраи делают выдачу токена детерминированной.
RESP="$(curl -s --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1 \
  -X POST "$TOKEN_URL" \
  -d grant_type=password \
  -d "client_id=$CLIENT_ID" \
  -d "username=$USER" \
  -d "password=$PASS")"

# Достаём access_token без jq (в базовом окружении его может не быть).
TOKEN="$(printf '%s' "$RESP" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
if [ -z "$TOKEN" ]; then
  echo "[get-token] токен не выдан. Ответ Keycloak:" >&2
  printf '%s\n' "$RESP" >&2
  exit 1
fi

printf '%s\n' "$TOKEN"

if [ "$DECODE" = 1 ]; then
  # payload = вторая часть JWT (base64url), выравниваем и декодируем. Подпись НЕ проверяется —
  # это только для наглядности claim'ов (проверку делают resource server'ы, Task 3/4).
  payload="$(printf '%s' "$TOKEN" | cut -d. -f2)"
  m=$(( ${#payload} % 4 )); [ "$m" -eq 2 ] && payload="${payload}=="; [ "$m" -eq 3 ] && payload="${payload}="
  echo "[get-token] декодированный payload:" >&2
  printf '%s' "$payload" | tr '_-' '/+' | { base64 -d 2>/dev/null || openssl base64 -d -A; } |
    { python -m json.tool 2>/dev/null || cat; } >&2
fi
