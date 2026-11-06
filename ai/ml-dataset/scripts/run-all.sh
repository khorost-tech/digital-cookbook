#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
python3 scripts/run-checks.py
python3 scripts/run-manifest.py
python3 scripts/run-marker.py
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/sabotage.py
