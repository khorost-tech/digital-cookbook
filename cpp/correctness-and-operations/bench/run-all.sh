#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда одной командой. Запускать из каталога
# стенда. Код возврата честный: первая упавшая ступень обрывает прогон, и
# маркер полного прогона не ставится.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

python3 scripts/run-tools.py
python3 scripts/probe.py
# раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
# прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
python3 scripts/run-defects.py
python3 scripts/analyze-defects.py  >/dev/null
python3 scripts/run-bench.py
python3 scripts/analyze-bench.py    >/dev/null
python3 scripts/run-shutdown.py
python3 scripts/analyze-shutdown.py >/dev/null
python3 scripts/run-config.py
python3 scripts/analyze-config.py   >/dev/null
python3 scripts/run-fds.py
python3 scripts/analyze-fds.py      >/dev/null
python3 scripts/run-logs.py
python3 scripts/analyze-logs.py     >/dev/null
python3 scripts/run-coverage.py
python3 scripts/analyze-coverage.py >/dev/null
python3 scripts/run-manifest.py
python3 scripts/run-marker.py       # последним: проверки и диверсии внутри
