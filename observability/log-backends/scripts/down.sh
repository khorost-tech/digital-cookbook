#!/usr/bin/env bash
# Остановить стенд. С аргументом clean — стереть и данные бэкендов.
#
#   ./scripts/down.sh          # погасить, данные сохранить
#   ./scripts/down.sh clean    # погасить и стереть тома
#
# Между замерами нужен именно clean. Без аргумента тома СОХРАНЯЮТСЯ, и замер
# «на чистых томах» пойдёт на грязных: остаточные записи прошлого прогона
# попадут и в счёт, и в объём. Ошибка стоила одного прогона в соседнем стенде
# этой серии.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

if [ "${1:-}" = "clean" ]; then
  log "останов с удалением томов"
  docker compose --profile "*" down -v --remove-orphans
else
  log "останов, тома сохранены"
  docker compose --profile "*" down --remove-orphans
fi
