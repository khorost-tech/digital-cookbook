#!/usr/bin/env bash
# Остановить стенд. С аргументом clean — стереть и данные бэкендов.
#
#   ./scripts/down.sh            # погасить, данные сохранить
#   ./scripts/down.sh clean      # погасить и стереть тома
#
# Между замерами нужен именно clean: остаточные трейсы и логи прошлого прогона
# ломают точные критерии проверок («столько трейсов, сколько запросов»).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cd "$STAND_DIR"

if [ "${1:-}" = "clean" ]; then
  log "останов с удалением томов"
  # --profile "*" обязателен: без него `docker compose down` НЕ трогает сервисы,
  # объявленные в профилях, и Pyroscope остаётся жить после «погасили стенд».
  # Проверено — контейнер ops-pyroscope переживал down и попадал в следующий
  # прогон со старыми профилями, что портило проверку свежести данных.
  docker compose --profile "*" down -v --remove-orphans
else
  log "останов, тома сохранены"
  docker compose --profile "*" down --remove-orphans
fi
