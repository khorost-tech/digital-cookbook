#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда performance/load-testing. Запускать из
# каталога стенда. Первая упавшая ступень обрывает прогон, и маркер не ставится.
# FRESH=1 — начать с пустых томов и пересобранных образов (вторая сессия).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ "${FRESH:-0}" = 1 ]; then
  docker compose --profile gen --profile chaos --profile soak down -v
  docker image rm -f load-testing-svc load-testing-gen >/dev/null 2>&1 || true
fi
for axis in model types ceiling chaos; do
  # раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
  # прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
  python3 scripts/run-$axis.py
  python3 scripts/analyze.py $axis >/dev/null
done
python3 scripts/run-manifest.py
python3 scripts/run-marker.py      # последним: проверки и диверсии внутри
