# flink — Apache Flink: глубокое погружение

Живые стенды к серии [«Apache Flink: глубокое погружение»](https://khorost.tech/messaging/flink-model-event-time-watermarks/) на khorost.tech.

Каждый подкаталог — самодостаточный стенд, поднимается одной командой `docker compose up -d`.
Демонстрации идут через **Flink SQL client** — не нужно ничего компилировать.

| Стенд | Что показывает | Статья |
|-------|----------------|--------|
| [`00-model`](00-model) | Event-time, watermarks и окна на встроенном `datagen` (без Kafka): tumbling/sliding-окна, опоздавшие события, сравнение event-time vs processing-time | [Модель Flink: dataflow, event-time, watermarks и окна](https://khorost.tech/messaging/flink-model-event-time-watermarks/) |
| [`01-state`](01-state) | Состояние на RocksDB, checkpoints и восстановление после падения TaskManager, exactly-once-сток в Kafka | [Состояние и exactly-once в Flink](https://khorost.tech/messaging/flink-state-exactly-once/) |
| [`02-sql-ops`](02-sql-ops) | Flink SQL: стриминговый джойн двух Kafka-топиков, savepoint и rescale (смена параллелизма) без потери состояния | [Flink SQL, коннекторы и эксплуатация](https://khorost.tech/messaging/flink-sql-connectors-operations/) |

## Версии

- Apache Flink `1.20.1` (Java 17)
- Apache Kafka `3.9.0` (KRaft, для `01-state` и `02-sql-ops`)
- Flink Kafka SQL connector `3.3.0-1.20`

Версии пиновать: поведение Flink (особенно state/checkpoint) меняется между минорами.

## Web UI

Flink Dashboard на каждом стенде: <http://localhost:8081> — джобы, чекпоинты, backpressure, метрики.

## Лицензия

MIT — см. [LICENSE](../LICENSE).
