#!/usr/bin/env bash
# Подъём стенда. Ждёт, пока схема встанет, роли поднимутся и namespace
# зарегистрируется, и только потом отдаёт управление.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

section "подъём стенда temporal"
docker compose -f "${COMPOSE_FILE}" up -d --wait postgres elasticsearch
docker compose -f "${COMPOSE_FILE}" up temporal-schema
docker compose -f "${COMPOSE_FILE}" up -d \
    temporal-frontend temporal-history temporal-matching temporal-worker \
    temporal-ui prometheus grafana
docker compose -f "${COMPOSE_FILE}" up temporal-namespace

echo
echo "Web UI:      http://localhost:8253"
echo "Prometheus:  http://localhost:9253"
echo "Grafana:     http://localhost:3253"
echo "gRPC (хост): localhost:7253   (для прогонов используйте сеть стенда!)"
