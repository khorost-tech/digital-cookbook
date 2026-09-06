#!/usr/bin/env bash
#
# call-apis.sh — прогнать матрицу авторизации против ОБОИХ resource server'ов
# (Go :8081 и Java :8082) и напечатать реальные HTTP-коды. Демонстрирует, что оба
# сервиса одинаково валидируют токены Keycloak и разграничивают доступ по realm-ролям.
#
# Матрица (ожидаемые коды):
#   GET /public            без токена   → 200  (открытый эндпоинт)
#   GET /me                без токена   → 401  (нужен валидный Bearer)
#   GET /me                alice        → 200  (любой валидный токен)
#   GET /admin             alice        → 403  (у alice нет роли admin)
#   GET /admin             bob          → 200  (у bob есть роль admin)
#   GET /me                битый токен  → 401  (подпись/формат не проходят)
#
# Токены получаются через scripts/get-token.sh (password grant, клиент cli):
#   alice / alice-demo-2026  → роль user
#   bob   / bob-demo-2026     → роли user + admin
#
# Требования: стенд поднят (scripts/up.sh), curl. Все креды — demo-only.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

GO_BASE="${GO_BASE:-http://localhost:8081}"
JAVA_BASE="${JAVA_BASE:-http://localhost:8082}"

log() { echo "[call-apis] $*" >&2; }

# HTTP-код запроса. --retry сглаживает «холодный» первый коннект через NAT Docker
# Desktop на Windows (первый запрос к проброшенному порту иногда отдаёт 000).
code() { # $1 = url ; $2 (опц.) = Bearer-токен
  local url="$1" tok="${2:-}"
  if [ -n "$tok" ]; then
    curl -s -o /dev/null -w '%{http_code}' \
      --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1 \
      -H "Authorization: Bearer $tok" "$url"
  else
    curl -s -o /dev/null -w '%{http_code}' \
      --retry 5 --retry-all-errors --retry-connrefused --retry-delay 1 "$url"
  fi
}

log "получаю токены (alice, bob)…"
ALICE="$(bash "$HERE/get-token.sh" alice)"
BOB="$(bash "$HERE/get-token.sh" bob)"
BAD="not.a.valid.token"

FAIL=0

# $1 base URL, $2 имя сервиса
run_suite() {
  local base="$1" name="$2"
  echo ""
  echo "=== $name ($base) ==="
  printf '%-28s %-8s %-8s %s\n' "сценарий" "код" "ожид" "результат"

  check() { # $1 описание, $2 фактический код, $3 ожидаемый
    local mark="OK"
    if [ "$2" != "$3" ]; then mark="ПРОВАЛ"; FAIL=1; fi
    printf '%-28s %-8s %-8s %s\n' "$1" "$2" "$3" "$mark"
  }

  check "/public (без токена)"   "$(code "$base/public")"           200
  check "/me (без токена)"       "$(code "$base/me")"               401
  check "/me (alice)"            "$(code "$base/me"    "$ALICE")"    200
  check "/admin (alice)"         "$(code "$base/admin" "$ALICE")"    403
  check "/admin (bob)"           "$(code "$base/admin" "$BOB")"      200
  check "/me (битый токен)"      "$(code "$base/me"    "$BAD")"      401
}

run_suite "$GO_BASE"   "Go resource server"
run_suite "$JAVA_BASE" "Java resource server"

echo ""
if [ "$FAIL" = 0 ]; then
  echo "[call-apis] ИТОГ: все сценарии совпали с ожиданием (200/401/403) на Go и Java."
else
  echo "[call-apis] ИТОГ: есть расхождения — см. строки с ПРОВАЛ выше." >&2
  exit 1
fi
