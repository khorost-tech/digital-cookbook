#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда grpc одной командой. Запускать из каталога
# стенда. Первая упавшая ступень обрывает прогон, и маркер не ставится.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

python3 scripts/run-protocol.py
# раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
# прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
python3 scripts/run-deadline.py
python3 scripts/analyze-deadline.py  >/dev/null
python3 scripts/run-cancel.py
python3 scripts/analyze-cancel.py    >/dev/null
python3 scripts/run-errors.py
python3 scripts/analyze-errors.py    >/dev/null
python3 scripts/run-chain.py
python3 scripts/analyze-chain.py     >/dev/null
python3 scripts/run-retry.py
python3 scripts/analyze-retry.py     >/dev/null
python3 scripts/run-keepalive.py
python3 scripts/analyze-keepalive.py >/dev/null
python3 scripts/run-balance.py
python3 scripts/analyze-balance.py   >/dev/null
python3 scripts/run-size.py
python3 scripts/analyze-size.py      >/dev/null
python3 scripts/run-manifest.py
python3 scripts/run-marker.py     # последним: проверки и диверсии внутри
