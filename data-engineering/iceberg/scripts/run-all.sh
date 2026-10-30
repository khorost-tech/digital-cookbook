#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда data-engineering/iceberg. Запускать из
# каталога стенда. Первая упавшая ступень обрывает прогон, и маркер не ставится.
# FRESH=1 — начать с пустых томов (вторая сессия).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ "${FRESH:-0}" = 1 ]; then docker compose down -v; fi
for axis in writers reader delete erase schema partitions maintenance; do
  # раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
  # прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
  python3 scripts/run-$axis.py
  python3 scripts/analyze.py $axis >/dev/null
done
python3 scripts/run-manifest.py
python3 scripts/run-marker.py      # последним: проверки и диверсии внутри
