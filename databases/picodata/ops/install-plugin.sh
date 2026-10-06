#!/usr/bin/env bash
# install-plugin.sh <версия> — устанавливает и включает плагин в кластере.
#
# Жизненный цикл плагина в Picodata состоит из четырёх шагов, и порядок важен:
#   CREATE PLUGIN            — кластер читает manifest.yaml из share_dir
#   ADD SERVICE ... TO TIER  — сервис привязывается к тиру (не к репликасету)
#   MIGRATE TO               — применяются миграции плагина
#   ENABLE                   — сервис запускается, срабатывает on_start
# Без MIGRATE включение падает с внятной ошибкой:
#   "cannot enable plugin `near_data:0.1.0`: need to apply migrations first
#    (applied 0/1)"
set -euo pipefail

VERSION="${1:?usage: install-plugin.sh <версия, например 0.1.0>}"
SERVICE="${SERVICE:-near_data_service}"
PICO_DSN="${PICO_DSN:-postgres://admin:Picodata1@picodata-1:5432/picodata?sslmode=disable}"

# psql берём из контейнера источника истины — клиент PostgreSQL работает с
# Picodata без каких-либо адаптеров, это и есть смысл PG-протокола.
q() { MSYS_NO_PATHCONV=1 docker exec picodata-origin psql "$PICO_DSN" -c "$1"; }

q "CREATE PLUGIN near_data ${VERSION}"
q "ALTER PLUGIN near_data ${VERSION} ADD SERVICE ${SERVICE} TO TIER default"
q "ALTER PLUGIN near_data MIGRATE TO ${VERSION}"
q "ALTER PLUGIN near_data ${VERSION} ENABLE"

MSYS_NO_PATHCONV=1 docker exec picodata-origin psql "$PICO_DSN" \
    -c "SELECT name, version, enabled FROM _pico_plugin"
