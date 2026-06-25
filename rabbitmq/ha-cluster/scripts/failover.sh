#!/usr/bin/env bash
set -euo pipefail
# Показываем: убиваем лидера quorum-очереди — потерь нет.
#
# Использование:
#   bash scripts/failover.sh          — интерактивный режим (паузы с Enter)
#   AUTO=1 bash scripts/failover.sh   — неинтерактивный режим (паузы пропускаются)
#   bash scripts/failover.sh --auto   — то же, что AUTO=1

if [[ "${1:-}" == "--auto" ]]; then
  AUTO=1
fi

pause() {
  if [ "${AUTO:-0}" = "1" ]; then return; fi
  read -rp "${1:-Нажмите Enter для продолжения...}" _
}

echo ">> Запустите в соседних терминалах:"
echo "   go run ./cmd/consumer"
echo "   go run ./cmd/producer -n 200"
pause ">> Когда producer и consumer запущены, нажмите Enter"

echo "== Лидер очереди demo.orders до сбоя =="
docker exec rabbit1 rabbitmqctl list_queues name type leader members | grep demo.orders || true

LEADER=$(docker exec rabbit1 rabbitmqctl list_queues name leader --quiet | awk '/demo.orders/ {print $2}')
echo "Лидер: ${LEADER:-<нет>}"

if [[ -z "$LEADER" ]]; then
  echo "Очередь demo.orders не найдена — сначала запустите producer, чтобы создать её"
  exit 1
fi

pause ">> Нажмите Enter, чтобы убить ноду-лидера ($LEADER)"

NODE_CONTAINER=$(echo "$LEADER" | sed 's/rabbit@//')
docker kill "$NODE_CONTAINER"
echo "Нода $NODE_CONTAINER убита. Наблюдайте: consumer продолжает получать сообщения."
echo "== Новый лидер =="
sleep 5

# Выбираем выжившую ноду для запроса — не ту, которую только что убили.
SURVIVOR=""
for node in rabbit1 rabbit2 rabbit3; do
  if [[ "$node" != "$NODE_CONTAINER" ]]; then
    SURVIVOR="$node"
    break
  fi
done
echo "(опрашиваем выжившую ноду: $SURVIVOR)"
docker exec "$SURVIVOR" rabbitmqctl list_queues name type leader members | grep demo.orders || true
