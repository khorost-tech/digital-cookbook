#!/usr/bin/env bash
# down.sh — снести стенд вместе с томами.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
docker compose -f compose/compose.yml down -v
