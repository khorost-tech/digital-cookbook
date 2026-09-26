#!/usr/bin/env bash
# Usage: ./run.sh sql/file.sql  → psql к ТЕКУЩЕМУ лидеру через HAProxy (порт 5000).
# psql берём из контейнера patroni1 (в образе haproxy его нет).
set -euo pipefail
docker compose exec -T patroni1 \
  psql "host=haproxy port=5000 user=postgres password=hapass dbname=postgres" \
  -v ON_ERROR_STOP=1 -f - < "$1"
