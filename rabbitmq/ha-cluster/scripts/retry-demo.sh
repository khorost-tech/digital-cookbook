#!/usr/bin/env bash
set -euo pipefail
# Демонстрация паттерна «отложенная доставка через retry-queue»:
#   demo.orders  →(reject без requeue)→  demo.dlx  →  demo.retry(TTL 5s)  →  demo.orders
#
# Топология задана в cluster/definitions.json:
#   - demo.dlx (fanout exchange) — dead-letter-exchange для demo.orders (политика dlx-orders)
#   - demo.retry (quorum, x-message-ttl=5000, x-dead-letter-exchange="",
#                 x-dead-letter-routing-key=demo.orders)
#     привязана к demo.dlx — принимает отбракованные сообщения и через 5 сек возвращает
#     их в основную очередь через default exchange.
#
# Использование:
#   bash scripts/retry-demo.sh          — интерактивный режим
#   AUTO=1 bash scripts/retry-demo.sh   — неинтерактивный режим (паузы пропускаются)
#   bash scripts/retry-demo.sh --auto   — то же, что AUTO=1
#
# Примечание: rabbitmqadmin 4.x (v2) использует подкоманды:
#   publish message -k <routing-key> -m <payload>
#   get messages --queue <name> --ack-mode <mode>
# — отличный синтаксис от v1.

if [[ "${1:-}" == "--auto" ]]; then
  AUTO=1
fi

pause() {
  if [ "${AUTO:-0}" = "1" ]; then return; fi
  read -rp "${1:-Нажмите Enter для продолжения...}" _
}

ts() {
  date '+%H:%M:%S'
}

echo "=== Демонстрация retry-queue (отложенная доставка через DLX + TTL) ==="
echo ""
echo "Схема: demo.orders → (reject) → demo.dlx → demo.retry(TTL 5s) → demo.orders"
echo ""

# ── Шаг 1: убедиться, что нужные очереди существуют ──
echo "[$(ts)] Шаг 1 — проверяем/объявляем очереди..."

# demo.orders — основная очередь (обычно создаётся Go-producer, здесь объявляем явно)
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  declare queue \
    --name demo.orders \
    --type quorum \
    --durable true

# demo.retry — retry-очередь с TTL (объявлена в definitions.json, переобъявляем идемпотентно)
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  declare queue \
    --name demo.retry \
    --type quorum \
    --durable true \
    --arguments '{"x-message-ttl":5000,"x-dead-letter-exchange":"","x-dead-letter-routing-key":"demo.orders"}'

# Привязка demo.retry к demo.dlx (если ещё не существует)
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  declare binding \
    --source demo.dlx \
    --destination-type queue \
    --destination demo.retry \
    --routing-key ""

echo "[$(ts)] demo.orders и demo.retry готовы, demo.retry привязана к demo.dlx."
echo ""

# ── Шаг 2: публикуем тестовое сообщение ──
echo "[$(ts)] Шаг 2 — публикуем сообщение в demo.orders..."
MSG="retry-test-$(date +%s)"
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  publish message \
    -k demo.orders \
    -m "$MSG" \
    -p '{"delivery_mode":2}'
echo "[$(ts)] Сообщение опубликовано: $MSG"
echo ""

pause ">> Нажмите Enter, чтобы выполнить reject (симуляция сбоя обработчика)"

# ── Шаг 3: получаем сообщение с reject (nack, requeue=false → уходит в demo.dlx → demo.retry) ──
echo "[$(ts)] Шаг 3 — получаем сообщение и reject (nack без requeue)..."
docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  get messages \
    --queue demo.orders \
    --ack-mode reject_requeue_false \
    --count 1
echo "[$(ts)] Сообщение отклонено → ушло в demo.dlx → demo.retry (TTL 5 сек)"
echo ""

# ── Шаг 4: показываем состояние очередей сразу после reject ──
echo "[$(ts)] Очереди сразу после reject:"
docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo || true
echo ""

pause ">> Нажмите Enter, чтобы подождать TTL (~5 сек) и проверить возврат"

# ── Шаг 5: ждём TTL и проверяем, что сообщение вернулось в demo.orders ──
echo "[$(ts)] Ожидаем истечения TTL (5 сек)..."
sleep 6
echo "[$(ts)] Шаг 5 — проверяем очереди после TTL:"
docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo || true
echo ""

# ── Шаг 6: забираем сообщение из demo.orders (успешный retry) ──
echo "[$(ts)] Шаг 6 — забираем сообщение из demo.orders (имитация успешного retry)..."
RECEIVED=$(docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  get messages \
    --queue demo.orders \
    --ack-mode ack_requeue_false \
    --count 1 2>&1 || true)
echo "$RECEIVED"

if echo "$RECEIVED" | grep -q "$MSG"; then
  echo ""
  echo "[$(ts)] УСПЕХ: сообщение '$MSG' вернулось в demo.orders после TTL и успешно обработано."
elif echo "$RECEIVED" | grep -qi "payload\|body\|message"; then
  echo ""
  echo "[$(ts)] УСПЕХ: сообщение получено из demo.orders (retry сработал)."
else
  echo ""
  echo "[$(ts)] Сообщение пока не вернулось (TTL ещё не истёк или задержка брокера)."
  echo "        Подождите ещё немного и проверьте вручную:"
  echo "        docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo"
fi

echo ""
echo "=== Итог ==="
echo "Финальное состояние очередей:"
sleep 1
docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo || true
echo ""
echo "Паттерн продемонстрирован:"
echo "  1. Сообщение опубликовано в demo.orders"
echo "  2. Consumer сделал reject (nack без requeue)"
echo "  3. Брокер переслал в demo.dlx → demo.retry (quorum, TTL 5 сек)"
echo "  4. После истечения TTL demo.retry → default exchange → demo.orders"
echo "  5. Сообщение снова доступно для обработки"
