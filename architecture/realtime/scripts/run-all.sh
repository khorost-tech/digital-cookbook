#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда одной командой. Запускать из каталога
# стенда. Код возврата честный: первая упавшая ступень обрывает прогон, и
# маркер полного прогона не ставится.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

python3 scripts/run-protocol.py
python3 scripts/run-flate.py
# раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
# прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
python3 scripts/run-wire.py
python3 scripts/analyze-wire.py      >/dev/null
python3 scripts/run-proxy.py
python3 scripts/analyze-proxy.py     >/dev/null
python3 scripts/run-scale.py
python3 scripts/analyze-scale.py     >/dev/null
python3 scripts/run-reconnect.py
python3 scripts/analyze-reconnect.py >/dev/null
python3 scripts/run-slow.py
python3 scripts/analyze-slow.py      >/dev/null
python3 scripts/run-idle.py
python3 scripts/analyze-idle.py      >/dev/null
python3 scripts/run-manifest.py
python3 scripts/run-marker.py     # последним: проверки и диверсии внутри
