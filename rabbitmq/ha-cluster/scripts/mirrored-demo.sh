#!/usr/bin/env bash
set -euo pipefail
# Только на 3.13: classic mirrored queues (в 4.x ha-mode игнорируется).
#
# На RabbitMQ 3.13 rabbitmqadmin — версия v1 (старый синтаксис: -u/-p, positional args).
# На RabbitMQ 4.x rabbitmqadmin v2 имеет другой синтаксис (--username/--password, --name и т.д.)

docker exec rabbit-legacy rabbitmqctl set_policy ha-all "^legacy\." \
  '{"ha-mode":"all","ha-sync-mode":"automatic"}'

# rabbitmqadmin v1 (RabbitMQ 3.13): используем старый синтаксис -u/-p
docker exec rabbit-legacy rabbitmqadmin -u demo -p demo declare queue name=legacy.q durable=true

echo "Политика ha-all применена. На 4.x эта же команда не дала бы зеркалирования."
echo "Миграция mirrored -> quorum: пересоздать очередь с x-queue-type=quorum, переключить producer/consumer, удалить ha-policy."
