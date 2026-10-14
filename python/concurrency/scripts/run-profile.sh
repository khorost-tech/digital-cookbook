#!/usr/bin/env bash
# Профилирование одной и той же нагрузки двумя разными инструментами.
#
# Вопрос сценария не "какой профайлер точнее", а "почему они показывают
# разное". Детерминированный профайлер перехватывает каждый вызов и потому
# завышает долю мелких частых функций. Сэмплирующий снимает стек по таймеру
# и о числе вызовов не знает. На нагрузке, где есть и то и другое, картины
# расходятся предсказуемо.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

PREFLIGHT="$(bash "$(dirname "$0")/preflight.sh")"
echo "$PREFLIGHT"

ITERS="${ITERS:-3000}"
ROUNDS="${ROUNDS:-5}"
RATE="${RATE:-200}"
PY=/opt/py/gil/bin/python
out=fixtures/profile.txt

dc() { docker compose exec -T bench "$@" 2>&1 | tr -d '\r'; }

{
  echo "# Профилирование, сборка: gil"
  echo "# iters=${ITERS} rounds=${ROUNDS} частота сэмплирования py-spy=${RATE} Гц"
  echo "# ядро: $(dc uname -sr)"
  echo "# снято: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# среда: ${PREFLIGHT}"
  echo "#"
  echo "## Цена инструмента: время того же прогона"
} > "$out"

# Весь цикл выполняется одним вызовом внутри контейнера: цепочка из
# десятков docker compose exec из внешней оболочки оказалась хрупкой —
# оболочка молча умирала на середине, не оставляя ни строки в stderr.
docker compose exec -T -e ITERS="$ITERS" -e ROUNDS="$ROUNDS" -e RATE="$RATE" bench sh /bench/profile_loop.sh

echo "готово: $out"
