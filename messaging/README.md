# Messaging — примеры

Брокеры сообщений и потоковая обработка: RabbitMQ, NATS, Kafka, Flink.

| Стенд | Описание | Статья |
|---|---|---|
| [`data-streaming/`](data-streaming/) | Сквозной пайплайн: PostgreSQL → Debezium/Kafka Connect → Kafka → Kafka Streams → ClickHouse | 🔜 скоро |
| [`flink/`](flink/) | Apache Flink 1.20 через Flink SQL: event-time, state, exactly-once | [статья](https://khorost.tech/messaging/flink-model-event-time-watermarks/) |
| [`kafka/`](kafka/) | Kafka: глубокое погружение (клиенты, ecosystem, MM2, ops, EOS) | [статья](https://khorost.tech/messaging/) |
| [`nats/`](nats/) | NATS 2.12: Core, JetStream, кластер, гео, безопасность, клиенты | [статья](https://khorost.tech/messaging/nats-core-subjects-request-reply/) |
| [`rabbitmq/`](rabbitmq/) | RabbitMQ 4.x: HA-кластер (quorum, DLQ, federation) и Streams | [статья](https://khorost.tech/messaging/rabbitmq-ha-cluster-quorum-failover/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
