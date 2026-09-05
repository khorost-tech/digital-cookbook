# Architecture — примеры

Событийные паттерны, распределённые транзакции, координация, устойчивость, тестирование распределённых систем.

| Стенд | Описание | Статья |
|---|---|---|
| [`cqrs/`](cqrs/) | CQRS на практике: разделение чтения и записи | [статья](https://khorost.tech/architecture/cqrs-in-practice/) |
| [`distributed-config/`](distributed-config/) | etcd, ZooKeeper, Consul, Vault: watch, discovery, dynamic credentials | [статья](https://khorost.tech/architecture/distributed-configuration/) |
| [`event-coordination/`](event-coordination/) | Хореография vs оркестрация | [статья](https://khorost.tech/architecture/choreography-vs-orchestration/) |
| [`event-payload/`](event-payload/) | Notification vs event-carried state transfer | [статья](https://khorost.tech/architecture/event-notification-vs-state-transfer/) |
| [`event-sourcing/`](event-sourcing/) | Event Sourcing на практике: store, агрегаты, проекции | [статья](https://khorost.tech/architecture/event-sourcing-in-practice/) |
| [`idempotency/`](idempotency/) | Гарантии доставки и идемпотентность (effectively-once) | [статья](https://khorost.tech/architecture/delivery-guarantees-idempotency/) |
| [`saga/`](saga/) | Saga на практике: локальные транзакции + компенсации, оркестрация | [статья](https://khorost.tech/architecture/saga-in-practice/) |
| [`serialization-formats/`](serialization-formats/) | JSON, Avro, Protobuf и JSON Schema на одних и тех же записях: размер и сжатие, эволюция схемы (девять изменений, тихая порча против явного отказа), что нужно иметь под рукой для чтения (реестр схем), и перекрёстное чтение байтов между независимыми Go- и Java-реализациями | [статья](https://khorost.tech/architecture/serialization-formats-json-avro-protobuf/) |
| [`temporal/`](temporal/) | Temporal: durable execution вглубь | [статья](https://khorost.tech/architecture/temporal-durable-workflows/) |
| [`testing/`](testing/) | Тестирование: распределённые системы (Testcontainers), TDD/BDD, flaky-тесты | [статья](https://khorost.tech/architecture/flaky-tests-diagnose-and-fix/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
