#!/usr/bin/env bash
#
# up.sh — единый вход в dev-стенд Keycloak: поднять все сервисы, дождаться готовности
# Keycloak и обоих resource server'ов (Go и Java), напечатать полезные URL.
#
# Поднимает (docker-compose.yml): postgres, keycloak (start-dev + import realm demo),
# backend-go (:8081), backend-java (:8082), mock-oidc (:8083). Сервис `bench` под
# профилем bench НЕ стартует (нагрузчик запускается отдельно из scripts/bench.sh).
#
# Первый `up` собирает образы backend-go/backend-java (multi-stage) — это занимает
# время; повторные запуски используют кэш. Все креды — demo-only (см. README).
#
# Требования: docker (+ compose). Порты хоста: 8080, 8081, 8082, 8083, 9000.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

COMPOSE=(docker compose)

log() { echo "[up] $*" >&2; }

cid() { "${COMPOSE[@]}" ps -q "$1" 2>/dev/null; }

# Ждём, пока docker-healthcheck сервиса не станет healthy.
wait_healthy() { # $1 = service, $2 = попыток (по 3с)
  local svc="$1" tries="${2:-60}" id st
  for _ in $(seq 1 "$tries"); do
    id="$(cid "$svc")"
    if [ -n "$id" ]; then
      st="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id" 2>/dev/null || echo none)"
      [ "$st" = healthy ] && { log "$svc: healthy"; return 0; }
    fi
    sleep 3
  done
  log "ОШИБКА: сервис $svc не стал healthy — см. docker compose logs $svc"
  return 1
}

log "docker compose up -d --build (первый запуск собирает образы backend-go/java)…"
"${COMPOSE[@]}" up -d --build

# Keycloak поднимается дольше всех (импорт realm + Quarkus). Затем backend'ы, которые
# depends_on keycloak (service_healthy) — к моменту их проверки KC уже готов.
wait_healthy keycloak 60
wait_healthy backend-go 40
wait_healthy backend-java 40

cat >&2 <<'URLS'

Стенд поднят. Точки входа:

  Keycloak (auth / admin UI)   http://localhost:8080
    admin console              http://localhost:8080/admin/  (admin / admin — demo-only)
    realm demo (с хоста)       http://localhost:8080/realms/demo
    OIDC discovery             http://localhost:8080/realms/demo/.well-known/openid-configuration
      ! iss В ТОКЕНАХ          http://keycloak:8080/realms/demo  (KC_HOSTNAME=docker-hostname,
        а не localhost — бэкенд сверяет iss строго по этой строке; см. статью 2/3)
  Keycloak management          http://localhost:9000/health/ready , http://localhost:9000/metrics
  Go resource server           http://localhost:8081   (/public /me /admin /healthz)
  Java resource server         http://localhost:8082   (/public /me /admin /actuator/health)
  mock-OIDC (внешний IdP)       http://localhost:8083/default/.well-known/openid-configuration

Дальше:
  scripts/get-token.sh [bob|alice]   — получить access-token (password grant)
  scripts/call-apis.sh               — прогнать 200/401/403 против Go и Java
  scripts/broker-demo.sh             — identity brokering через mock-OIDC
  scripts/bench.sh                   — JWKS vs introspection (нагрузка)
  scripts/down.sh                    — остановить и удалить стенд

URLS
