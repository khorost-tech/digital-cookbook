#!/usr/bin/env bash
# orders.events ДОЛЖЕН существовать с 3 партициями ДО регистрации Debezium-
# коннектора (connect/debezium-outbox.json), а customer.totals ДОЛЖЕН
# существовать с 1 партицией ДО первого запуска Kafka Streams (streams/ —
# он пишет в customer.totals как в выходной топик). Запускать СРАЗУ после
# `docker compose up -d`, ДО curl .../connectors и ДО mvn exec:java.
#
# Почему это отдельный явный шаг, а не доверие auto.create.topics.enable:
# ни один сервис в compose.yml не создаёт эти топики заранее — их создаёт
# ПЕРВЫЙ, кто в них пишет (для orders.events — Kafka Connect/EventRouter
# SMT, для customer.totals — продюсер Kafka Streams). Auto-create берёт
# брокерский дефолт num.partitions=1 (compose.yml его не переопределяет,
# `grep num.partitions compose/compose.yml` пуст), и для двух топиков этот
# дефолт значит ПРОТИВОПОЛОЖНОЕ:
#
#   - orders.events: при 1 партиции баг co-partitioning из Задачи 3 (Kafka
#     Streams, streams/.../StreamsApp.java) физически не может произойти —
#     заказы одного клиента с разными order_id никогда не окажутся в
#     разных task'ах, потому что task один. Демонстрация из Задачи 3
#     ("ДОКАЗАНО на 3 партициях: события 28/20/12") без явного создания
#     топика невоспроизводима: auto-create даст 1 партицию, и артефакт
#     co-partitioning окажется недостижим на свежем стенде. Нужно ЯВНО 3.
#
#   - customer.totals: наоборот, ДОЛЖЕН остаться на 1 партиции — этого как
#     раз хватает по умолчанию, но полагаться на умолчание нельзя.
#     sql/02-clickhouse.sql прямо документирует, что version=offset в
#     ReplacingMergeTree(version) корректен ТОЛЬКО при 1 партиции (offset
#     монотонен только в пределах одной партиции; при нескольких —
#     несравним, и дедуп начнёт выбирать между дублями произвольно, а не
#     заведомо "последнюю" запись). Читатель учебного стенда про
#     партиционирование вполне может выставить где-то num.partitions=3 —
#     если customer.totals не закреплён явно здесь, доказательство
#     Critical 1 (дедуплицированная сумма = PG) развалится МОЛЧА, без
#     единой ошибки (см. отчёт: 312.09 вместо 12930.04 при argMin/any
#     вместо version=offset — тот же класс дефекта, только источник другой).
#
# --if-not-exists: идемпотентно при повторных запусках (например, если этот
# скрипт уже выполнялся и сейчас запускается снова по ошибке).
set -euo pipefail
docker exec ds-kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 \
  --create --if-not-exists \
  --topic orders.events --partitions 3 --replication-factor 1
echo "--- orders.events ---"
docker exec ds-kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --describe --topic orders.events

docker exec ds-kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 \
  --create --if-not-exists \
  --topic customer.totals --partitions 1 --replication-factor 1
echo "--- customer.totals ---"
docker exec ds-kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --describe --topic customer.totals
