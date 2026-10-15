#!/usr/bin/env bash
# Блокирующая проверка эквивалентности трёх приложений и двух контролей.
# Любое расхождение тел — отказ: без равенства работы замер сравнивал бы
# не фреймворки, а объём того, что они делают.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose exec -T bench /opt/py/web/bin/python /bench/check_equivalence.py
