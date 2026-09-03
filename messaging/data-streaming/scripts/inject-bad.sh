#!/usr/bin/env bash
# Кладёт заведомо непарсимое сообщение в customer.totals — проверка, что
# пайплайн переживает битое событие и отправляет его в DLQ.
#
# НЕОБРАТИМО: запись физически остаётся в топике навсегда (Kafka не умеет
# удалять отдельные сообщения). После первого запуска "чистый" baseline
# (битых=0) без полного docker compose down -v не воспроизвести, а каждый
# повторный `go run . -from-start` будет заново находить эту же запись и
# слать в DLQ ещё один дубль.
set -euo pipefail
echo 'not-a-json{{{' | docker exec -i ds-kafka /opt/kafka/bin/kafka-console-producer.sh \
  --bootstrap-server localhost:9092 --topic customer.totals
echo "битое событие отправлено в customer.totals"
