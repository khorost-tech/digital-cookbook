#!/usr/bin/env bash
set -euo pipefail
# demo.orders имеет dead-letter-exchange=demo.dlx и delivery-limit=5 (из definitions.json).
# Создаём DLQ и привязываем к demo.dlx, публикуем и nack'аем сверх лимита.
#
# Использование:
#   bash scripts/dlq-demo.sh          — запуск
#   AUTO=1 bash scripts/dlq-demo.sh   — неинтерактивный режим (паузы пропускаются)
#   bash scripts/dlq-demo.sh --auto   — то же, что AUTO=1
#
# ВАЖНО: rabbitmqadmin 4.x (v2) использует синтаксис с именованными флагами,
# отличный от v1: --username/--password вместо -u/-p, подкоманды с --name и --type.

if [[ "${1:-}" == "--auto" ]]; then
  AUTO=1
fi

pause() {
  if [ "${AUTO:-0}" = "1" ]; then return; fi
  read -rp "${1:-Нажмите Enter для продолжения...}" _
}

docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  declare queue \
    --name demo.dlq \
    --type quorum \
    --durable true

docker exec rabbit1 rabbitmqadmin \
  --username demo --password demo \
  declare binding \
    --source demo.dlx \
    --destination-type queue \
    --destination demo.dlq \
    --routing-key ""

echo "DLQ demo.dlq создана и привязана к demo.dlx."
echo ""
echo "=== Демонстрация dead-lettering через delivery-limit (poison message) ==="
echo ""
echo "Механизм: consumer -nack симулирует падение обработчика — получает сообщение"
echo "и закрывает соединение БЕЗ ack. Quorum-очередь видит unacked сообщение и"
echo "при следующем переподключении увеличивает x-delivery-count. После"
echo "delivery-limit=5 повторных доставок брокер автоматически отправляет"
echo "сообщение в demo.dlx → demo.dlq. Это защита от 'ядовитого сообщения':"
echo "бесконечная ретри-петля невозможна."
echo ""
echo "Шаг 1 — опубликовать несколько сообщений:"
echo "  cd go && go run ./cmd/producer -n 3"
echo ""
echo "Шаг 2 — запустить consumer с режимом -nack (симуляция падения при каждой доставке):"
echo "  cd go && go run ./cmd/consumer -nack"
echo "  (оставить работать ~15 секунд, затем Ctrl+C)"
echo ""
echo "  В логах будет видно: каждое сообщение получает доставку #1, #2 ... #5 (#6),"
echo "  после чего delivery-limit срабатывает и сообщение уходит в DLQ."
echo "  (delivery-limit=5 = до 5 redeliveries; итого до 6 доставок всего)"
echo ""
echo "Шаг 3 — убедиться, что сообщения появились в demo.dlq:"
echo "  docker exec rabbit1 rabbitmqctl list_queues name messages | grep demo"
