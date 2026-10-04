#!/usr/bin/env bash
set -euo pipefail
# Примечание (адаптация под Tarantool 3): `tarantool < indexes.lua` из исходного примера
# запускает НОВЫЙ процесс без bootstrap-конфига ("No cluster config received
# from the given configuration sources") — образ 3.x требует декларативный
# cluster config для отдельного инстанса. Вместо этого подключаемся через `tt
# connect` к уже запущенному (и сконфигурированному entrypoint'ом) инстансу
# и выполняем скрипт в его консоли.
docker compose exec -T tarantool tt connect localhost:3301 -f- < indexes.lua
