#!/usr/bin/env bash
#
# run-all.sh — полный прогон стенда golangci-lint одной командой. Запускать из
# каталога стенда. Первая упавшая ступень обрывает прогон, и маркер не ставится.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# раннер и разбор — отдельными командами: под set -e ошибка в левой части &&
# прогон не обрывает, и разбор шёл бы по фикстуре прошлой сессии
python3 scripts/run-default.py
python3 scripts/analyze-default.py  >/dev/null
python3 scripts/run-truncate.py
python3 scripts/analyze-truncate.py >/dev/null
python3 scripts/run-exclude.py
python3 scripts/analyze-exclude.py  >/dev/null
python3 scripts/run-nolint.py
python3 scripts/analyze-nolint.py   >/dev/null
python3 scripts/run-legacy.py
python3 scripts/analyze-legacy.py   >/dev/null
python3 scripts/run-custom.py
python3 scripts/analyze-custom.py   >/dev/null
python3 scripts/run-semantics.py
python3 scripts/analyze-semantics.py >/dev/null
python3 scripts/run-trainer.py
python3 scripts/analyze-trainer.py  >/dev/null
python3 scripts/run-manifest.py
python3 scripts/run-marker.py     # последним: проверки и диверсии внутри
