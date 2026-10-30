#!/bin/sh
# Грейдер тренажёра: ./check.sh l1 (уровни l1–l4). Нужны Docker и Python 3.
exec python3 "$(dirname "$0")/check.py" "$@"
