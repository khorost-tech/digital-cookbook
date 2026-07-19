#!/usr/bin/env bash
set -euo pipefail
docker compose exec -T mongo mongosh --quiet < indexes.js
