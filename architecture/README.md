# Architecture — примеры

Событийные паттерны, распределённые транзакции, координация, устойчивость, тестирование распределённых систем.

| Стенд | Описание | Статья |
|---|---|---|
| [`background-jobs/`](background-jobs/) | Фоновые задачи: очередь на PostgreSQL (аренда, SKIP LOCKED, LISTEN/NOTIFY, bloat, потолок), River, планировщик на advisory-локе, k8s CronJob | [статья](https://khorost.tech/architecture/task-queues-background-jobs/) |
| [`cqrs/`](cqrs/) | CQRS на практике: разделение чтения и записи | [статья](https://khorost.tech/architecture/cqrs-in-practice/) |
| [`distributed-config/`](distributed-config/) | etcd, ZooKeeper, Consul, Vault: watch, discovery, dynamic credentials | [статья](https://khorost.tech/architecture/distributed-configuration/) |
| [`event-coordination/`](event-coordination/) | Хореография vs оркестрация | [статья](https://khorost.tech/architecture/choreography-vs-orchestration/) |
| [`event-payload/`](event-payload/) | Notification vs event-carried state transfer | [статья](https://khorost.tech/architecture/event-notification-vs-state-transfer/) |
| [`event-sourcing/`](event-sourcing/) | Event Sourcing на практике: store, агрегаты, проекции | [статья](https://khorost.tech/architecture/event-sourcing-in-practice/) |
| [`event-sourcing-deep/`](event-sourcing-deep/) | Event store вглубь: Postgres, KurrentDB и Kafka на одном домене; границы агрегата, проекции с чекпоинтами, upcasting, given-when-then | 🔜 скоро |
| [`grpc/`](grpc/) | gRPC на Go и Java перекрёстно, за Angie и Envoy: доходит ли deadline до нижнего звена, останавливается ли стрим после отмены, коды ошибок и паника, порядок интерсепторов, встроенные ретраи, keepalive и GOAWAY, балансировка долгого HTTP/2-соединения за L4 и L7, gRPC-Gateway — счётом исходов | [статья](https://khorost.tech/architecture/grpc-deep-dive/) |
| [`idempotency/`](idempotency/) | Гарантии доставки и идемпотентность (effectively-once) | [статья](https://khorost.tech/architecture/delivery-guarantees-idempotency/) |
| [`realtime/`](realtime/) | WebSocket, SSE и polling на Go и Java за Angie, fan-out через Redis и NATS: байты на событие, буферизация и таймауты прокси, доставка между инстансами, потери при разрыве и догон по Last-Event-ID, медленный клиент, память на соединение — счётом исходов | [статья](https://khorost.tech/architecture/realtime-websockets-sse/) |
| [`resilience/`](resilience/) | Backpressure и load shedding: bounded-очередь, AIMD-лимитер | [статья](https://khorost.tech/architecture/backpressure-load-shedding/) |
| [`saga/`](saga/) | Saga на практике: локальные транзакции + компенсации, оркестрация | [статья](https://khorost.tech/architecture/saga-in-practice/) |
| [`serialization-formats/`](serialization-formats/) | JSON, Avro, Protobuf и JSON Schema на одних и тех же записях: размер и сжатие, эволюция схемы (девять изменений, тихая порча против явного отказа), что нужно иметь под рукой для чтения (реестр схем), и перекрёстное чтение байтов между независимыми Go- и Java-реализациями | [статья](https://khorost.tech/architecture/serialization-formats-json-avro-protobuf/) |
| [`temporal/`](temporal/) | Temporal: durable execution вглубь | [статья](https://khorost.tech/architecture/temporal-durable-workflows/) |
| [`testing/`](testing/) | Тестирование: распределённые системы (Testcontainers), TDD/BDD, flaky-тесты | [статья](https://khorost.tech/architecture/flaky-tests-diagnose-and-fix/) |
| [`schema-migrators/`](schema-migrators/) | goose, Flyway, Liquibase, Atlas и Alembic против PostgreSQL, CockroachDB и Picodata: журнал версий, применение, интроспекция и откат с проверкой состояния базы SQL-запросом; обходные пути для CockroachDB | [статья](https://khorost.tech/architecture/schema-migrators-across-databases/) |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
