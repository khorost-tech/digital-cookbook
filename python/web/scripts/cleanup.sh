#!/usr/bin/env bash
# Погасить серверы, оставшиеся от прерванного прогона.
#
# preflight намеренно ОТКАЗЫВАЕТ при живых процессах python, а не подчищает
# их сам: молчаливая уборка скрыла бы, что предыдущий прогон не доработал, и
# его частичные данные остались бы в фикстуре незамеченными. Уборка — явное
# действие, и вот оно.
set -euo pipefail

cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1

before=$(docker compose exec -T bench sh -c 'ps -eo comm= | grep -c "^python" || true' | tr -d '\r')
echo "процессов python до уборки: ${before}"

# restart надёжнее pkill: уносит и то, что успело осиротеть.
docker compose restart >/dev/null
sleep 3

after=$(docker compose exec -T bench sh -c 'ps -eo comm= | grep -c "^python" || true' | tr -d '\r')
echo "после: ${after}"
[ "$after" -eq 0 ] || { echo "УБОРКА НЕ УДАЛАСЬ: осталось ${after}" >&2; exit 1; }

echo "ВАЖНО: фикстуру прерванного прогона надо удалить вручную — частичные"
echo "данные в ней выглядят как полные."
