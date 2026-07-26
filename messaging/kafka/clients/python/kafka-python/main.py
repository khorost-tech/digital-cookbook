#!/usr/bin/env python3
"""Стенд #8 (клиенты в разных языках), Python: kafka-python(-ng).

Родословная: ЧИСТАЯ реализация протокола Kafka на Python (без librdkafka,
без CGo-аналога). Используется актуально поддерживаемый форк
`kafka-python-ng` (PyPI-пакет `kafka-python` де-факто не обновлялся — форк
активнее, но публичный API идентичен `import kafka`).

⚠️ Проверено живьём по докам/исходнику (kafka.KafkaProducer, v2.2.3): метод
конструктора НЕ имеет отдельного флага "enable.idempotence" (в отличие от
confluent-kafka/librdkafka). У этой версии есть `acks='all'` и ручной
`retries`, но нет полноценного идемпотентного producer API, как у
JVM-референса или librdkafka-обёрток — честно зафиксировано, не
сымитировано (см. тот же паритет фич, что и у segmentio/kafka-go в Go).

Сценарий: producer с ключом -> consumer в group с ручным коммитом, печатает
(partition, offset, key).

Запуск:
    docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/python/kafka-python:/app" -w /app python:3.12 \
      sh -c "pip install -q kafka-python-ng==2.2.3 && python main.py kafka1:9092,kafka2:9092,kafka3:9092"
"""
import sys
import time

from kafka import KafkaProducer, KafkaConsumer, KafkaAdminClient
from kafka.admin import NewTopic
from kafka.errors import UnknownTopicOrPartitionError

TOPIC = "demo-clients-python-kafkapython"
PARTITIONS = 3
REPLICATION = 3
GROUP_ID = "demo-clients-python-kafkapython-group"
KEYS = ["order-1", "order-2", "order-3", "order-4"]


def ensure_topic(brokers: str) -> None:
    admin = KafkaAdminClient(bootstrap_servers=brokers)
    try:
        admin.delete_topics([TOPIC], timeout_ms=20000)
    except UnknownTopicOrPartitionError:
        pass  # первый запуск

    deadline = time.time() + 10
    while time.time() < deadline:
        if TOPIC not in admin.list_topics():
            break
        time.sleep(0.3)

    admin.create_topics([NewTopic(name=TOPIC, num_partitions=PARTITIONS, replication_factor=REPLICATION)])
    admin.close()
    print(f"[admin] топик {TOPIC} создан (partitions={PARTITIONS}, rf={REPLICATION})")


def produce(brokers: str) -> int:
    producer = KafkaProducer(
        bootstrap_servers=brokers,
        acks="all",
        key_serializer=lambda k: k.encode(),
        value_serializer=lambda v: v.encode(),
    )

    count = 0
    for round_ in range(3):
        for key in KEYS:
            value = f"{key}-evt-{round_}"
            future = producer.send(TOPIC, key=key, value=value)
            md = future.get(timeout=10)  # синхронно дожидаемся подтверждения
            print(f"  sent  key={key} partition={md.partition} offset={md.offset}")
            count += 1
    producer.flush()
    producer.close()
    return count


def consume(brokers: str, expected: int) -> list[str]:
    consumer = KafkaConsumer(
        TOPIC,
        bootstrap_servers=brokers,
        group_id=GROUP_ID,
        auto_offset_reset="earliest",
        enable_auto_commit=False,
        key_deserializer=lambda k: k.decode() if k else None,
        value_deserializer=lambda v: v.decode() if v else None,
        consumer_timeout_ms=30000,
    )

    recs = []
    for msg in consumer:
        recs.append((msg.partition, msg.offset, msg.key))
        consumer.commit()  # ручной синхронный коммит offset ПОСЛЕ обработки каждой записи
        if len(recs) >= expected:
            break
    consumer.close()

    if len(recs) < expected:
        raise RuntimeError(f"consume: таймаут, получено {len(recs)} из {expected}")

    recs.sort(key=lambda r: (r[0], r[1]))
    out = []
    for partition, offset, key in recs:
        line = f"(partition={partition}, offset={offset}, key={key})"
        print(f"  recv  {line}")
        out.append(line)
    return out


def main() -> None:
    brokers = sys.argv[1] if len(sys.argv) > 1 else "kafka1:9092,kafka2:9092,kafka3:9092"

    ensure_topic(brokers)
    sent = produce(brokers)
    print(f"[producer] отправлено (acks=all, БЕЗ идемпотентности — не поддерживается драйвером): {sent}")
    recv = consume(brokers, sent)
    print(f"[consumer] получено (group={GROUP_ID}, manual commit): {len(recv)}")

    if sent != len(recv):
        raise SystemExit(f"[assert] FAIL: отправлено {sent} != получено {len(recv)}")
    print("[assert] OK: отправлено == получено")


if __name__ == "__main__":
    main()
