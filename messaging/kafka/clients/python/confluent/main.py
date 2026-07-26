#!/usr/bin/env python3
"""Стенд #8 (клиенты в разных языках), Python: confluent-kafka (librdkafka).

Родословная: обёртка над librdkafka (C) — тот же движок, что у Go
confluent-kafka-go, C# Confluent.Kafka, Rust rdkafka, C++ modern-cpp-kafka.
Нужна нативная зависимость (librdkafka.so, тянется как manylinux wheel —
pip НЕ требует системного librdkafka-dev, в отличие от Go/CGo варианта).

Сценарий: producer с ключом+идемпотентностью -> consumer в group с ручным
коммитом, печатает (partition, offset, key).

Запуск:
    docker run --rm --network kafka-cookbook-net -v "$(pwd)/clients/python/confluent:/app" -w /app python:3.12 \
      sh -c "pip install -q confluent-kafka==2.6.1 && python main.py kafka1:9092,kafka2:9092,kafka3:9092"
"""
import sys
import time

from confluent_kafka import Producer, Consumer, KafkaException
from confluent_kafka.admin import AdminClient, NewTopic

TOPIC = "demo-clients-python-confluent"
PARTITIONS = 3
REPLICATION = 3
GROUP_ID = "demo-clients-python-confluent-group"
KEYS = ["order-1", "order-2", "order-3", "order-4"]


def ensure_topic(brokers: str) -> None:
    admin = AdminClient({"bootstrap.servers": brokers})
    fs = admin.delete_topics([TOPIC], operation_timeout=20)
    for topic, f in fs.items():
        try:
            f.result()
        except KafkaException:
            pass  # unknown topic при первом запуске

    deadline = time.time() + 10
    while time.time() < deadline:
        md = admin.list_topics(timeout=5)
        if TOPIC not in md.topics:
            break
        time.sleep(0.3)

    fs = admin.create_topics([NewTopic(TOPIC, num_partitions=PARTITIONS, replication_factor=REPLICATION)])
    for topic, f in fs.items():
        f.result()
    print(f"[admin] топик {TOPIC} создан (partitions={PARTITIONS}, rf={REPLICATION})")


def produce(brokers: str) -> int:
    p = Producer({
        "bootstrap.servers": brokers,
        "acks": "all",
        "enable.idempotence": True,
    })

    count = 0
    errors = []

    def on_delivery(err, msg):
        if err is not None:
            errors.append(err)
            return
        print(f"  sent  key={msg.key().decode()} partition={msg.partition()} offset={msg.offset()}")

    for round_ in range(3):
        for key in KEYS:
            value = f"{key}-evt-{round_}"
            p.produce(TOPIC, key=key, value=value, callback=on_delivery)
            p.poll(0)
            count += 1
    p.flush(30)
    if errors:
        raise RuntimeError(f"delivery errors: {errors}")
    return count


def consume(brokers: str, expected: int) -> list[str]:
    c = Consumer({
        "bootstrap.servers": brokers,
        "group.id": GROUP_ID,
        "auto.offset.reset": "earliest",
        "enable.auto.commit": False,
        "partition.assignment.strategy": "cooperative-sticky",
    })
    c.subscribe([TOPIC])

    recs = []
    deadline = time.time() + 30
    try:
        while len(recs) < expected and time.time() < deadline:
            msg = c.poll(1.0)
            if msg is None:
                continue
            if msg.error():
                raise KafkaException(msg.error())
            recs.append((msg.partition(), msg.offset(), msg.key().decode()))
            c.commit(msg)  # ручной синхронный коммит offset ПОСЛЕ обработки каждой записи
    finally:
        c.close()

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
    print(f"[producer] отправлено (acks=all, enable.idempotence=true): {sent}")
    recv = consume(brokers, sent)
    print(f"[consumer] получено (group={GROUP_ID}, manual commit): {len(recv)}")

    if sent != len(recv):
        raise SystemExit(f"[assert] FAIL: отправлено {sent} != получено {len(recv)}")
    print("[assert] OK: отправлено == получено")


if __name__ == "__main__":
    main()
