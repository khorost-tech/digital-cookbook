#!/usr/bin/env bash
# Остановка стенда вместе с томом Postgres: история воркфлоу не должна
# перетекать между прогонами профилей.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

docker compose -f "${COMPOSE_FILE}" down -v --remove-orphans
