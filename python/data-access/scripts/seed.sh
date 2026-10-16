#!/usr/bin/env bash
# Посев базы. Отдельным скриптом, потому что делается редко и осознанно:
# пересев меняет набор данных, а значит обесценивает все снятые фикстуры.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

docker compose up -d db bench
docker compose exec -T bench sh -c 'cd /db && python seed.py'
echo "посев готов; сверка:"
docker compose exec -T bench sh -c 'cd /db && python seed.py --verify'
