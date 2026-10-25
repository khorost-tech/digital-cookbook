#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда data-engineering/pipelines. Запускать из
# каталога стенда. Первая упавшая ступень обрывает прогон, и маркер не ставится.
# FRESH=1 — начать с пустых томов (вторая сессия).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ "${FRESH:-0}" = 1 ]; then docker compose down -v; fi
# раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
# прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
python3 scripts/run-elt.py
python3 scripts/analyze-elt.py         >/dev/null
python3 scripts/run-idempotency.py
python3 scripts/analyze-idempotency.py >/dev/null
python3 scripts/run-interval.py
python3 scripts/analyze-interval.py    >/dev/null
python3 scripts/run-backfill.py
python3 scripts/analyze-backfill.py    >/dev/null
python3 scripts/run-late.py
python3 scripts/analyze-late.py        >/dev/null
python3 scripts/run-retries.py
python3 scripts/analyze-retries.py     >/dev/null
python3 scripts/run-manifest.py
python3 scripts/run-marker.py      # последним: проверки и диверсии внутри
