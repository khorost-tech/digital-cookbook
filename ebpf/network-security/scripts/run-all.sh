#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда ebpf/network-security. Запускать из
# каталога стенда. Первая упавшая ступень обрывает прогон, и маркер не ставится.
# FRESH=1 — начать с пустых томов (вторая сессия).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ "${FRESH:-0}" = 1 ]; then
  docker compose exec -T tools kind delete cluster --name netsec 2>/dev/null || true
  docker compose down -v
  rm -rf bin out cmd/*/*_bpfel.go cmd/*/*_bpfel.o   # собранное — заново из исходников
fi
for axis in filter cilium; do
  # отдельными командами: под set -e ошибка в левой части && прогон не обрывает,
  # и разбор шёл бы по фикстуре прошлой сессии
  python3 scripts/run-$axis.py
  python3 scripts/analyze.py $axis >/dev/null
done
python3 scripts/run-manifest.py
python3 scripts/run-marker.py      # последним: проверки и диверсии внутри
