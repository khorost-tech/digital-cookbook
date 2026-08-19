# Полный список примеров

Все стенды digital-cookbook со ссылками на статьи. Навигация по категориям — в [README.md](README.md).

## Architecture

| Пример | Описание | Статья |
|---|---|---|
| [`architecture/cqrs`](architecture/cqrs) | CQRS на практике: разделение чтения и записи | [статья](https://khorost.tech/architecture/cqrs-in-practice/) |
| [`architecture/distributed-config`](architecture/distributed-config) | etcd, ZooKeeper, Consul, Vault: watch, discovery, dynamic credentials | [статья](https://khorost.tech/architecture/distributed-configuration/) |
| [`architecture/event-coordination`](architecture/event-coordination) | Хореография vs оркестрация | [статья](https://khorost.tech/architecture/choreography-vs-orchestration/) |
| [`architecture/event-payload`](architecture/event-payload) | Notification vs event-carried state transfer | [статья](https://khorost.tech/architecture/event-notification-vs-state-transfer/) |
| [`architecture/event-sourcing`](architecture/event-sourcing) | Event Sourcing на практике: store, агрегаты, проекции | [статья](https://khorost.tech/architecture/event-sourcing-in-practice/) |
| [`architecture/idempotency`](architecture/idempotency) | Гарантии доставки и идемпотентность (effectively-once) | [статья](https://khorost.tech/architecture/delivery-guarantees-idempotency/) |
| [`architecture/saga`](architecture/saga) | Saga на практике: локальные транзакции + компенсации, оркестрация | [статья](https://khorost.tech/architecture/saga-in-practice/) |
| [`architecture/temporal`](architecture/temporal) | Temporal: durable execution вглубь | [статья](https://khorost.tech/architecture/temporal-durable-workflows/) |

## Databases

| Пример | Описание | Статья |
|---|---|---|
| [`databases/clickhouse`](databases/clickhouse) | ClickHouse и аналитические БД: MergeTree, MV, кластер, S3 | [статья](https://khorost.tech/databases/clickhouse-when-olap/) |
| [`databases/db-indexes`](databases/db-indexes) | Индексы в БД: PostgreSQL, MongoDB, Tarantool | [статья](https://khorost.tech/databases/) |
| [`databases/mongodb`](databases/mongodb) | MongoDB: глубокое погружение (модель, индексы, репликация, шардирование) | 🔜 скоро |
| [`databases/opensearch`](databases/opensearch) | OpenSearch: кластер, индексы, ingest, полнотекст, ISM, семантика, Dashboards | [статья](https://khorost.tech/infrastructure/opensearch-cluster-ansible/) |
| [`databases/postgres`](databases/postgres) | Клиенты Go/Java/Rust к PostgreSQL: primary+replica+pgbouncer, failover | [статья](https://khorost.tech/databases/postgres-clients-reliability-go-java-rust/) |
| [`databases/redis/client-resilience`](databases/redis/client-resilience) | Клиенты Go/Java/Rust к Redis: Cluster/Sentinel, reconnect и failover | [статья](https://khorost.tech/databases/redis-clients-go-java-rust/) |
| [`databases/redis/deep-dive`](databases/redis/deep-dive) | Redis/Valkey: глубокое погружение (кодировки, event loop, персистентность, Cluster/Sentinel, память, streams/Lua, эксплуатация) | [статья](https://khorost.tech/databases/) |
| [`databases/scylladb`](databases/scylladb) | ScyllaDB: глубокое погружение (топология, компакция, LWT, драйверы) | [статья](https://khorost.tech/databases/) |
| [`databases/transactions`](databases/transactions) | Транзакции и изоляция: реляционные, KV/документные, брокеры, мульти-хранилище | [статья](https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/) |
| [`databases/identifiers`](databases/identifiers) | Идентификаторы: локальность и bloat UUIDv4/UUIDv7/bigint в PostgreSQL, распределение в Mongo/Scylla, генерация в Go/Java/Rust | [статья](https://khorost.tech/databases/identifiers-index-locality-bloat/) |

## Messaging

| Пример | Описание | Статья |
|---|---|---|
| [`messaging/kafka`](messaging/kafka) | Kafka: глубокое погружение (клиенты, ecosystem, MM2, ops, EOS) | [статья](https://khorost.tech/messaging/) |
| [`messaging/nats`](messaging/nats) | NATS 2.12: Core, JetStream, кластер, гео, безопасность, клиенты | [статья](https://khorost.tech/messaging/nats-core-subjects-request-reply/) |
| [`messaging/rabbitmq`](messaging/rabbitmq) | RabbitMQ 4.x: HA-кластер (quorum, DLQ, federation) и Streams | [статья](https://khorost.tech/messaging/rabbitmq-ha-cluster-quorum-failover/) |

## Go

| Пример | Описание | Статья |
|---|---|---|
| [`go/asm`](go/asm) | Go assembly: dot product на AVX2/NEON, avo, ускорение ×3.88 | [статья](https://khorost.tech/go/) |
| [`go/concurrency`](go/concurrency) | Конкурентность в Go: горутины/каналы, sync, модель памяти, паттерны, отладка гонок | [статья](https://khorost.tech/go/go-concurrency-goroutines-channels/) |
| [`go/context`](go/context) | Контекст в Go: отмена, дедлайны, WithCancelCause, request-scoped values | 🔜 скоро |
| [`go/orm-gorm-vs-jet`](go/orm-gorm-vs-jet) | ORM в Go: GORM vs go-jet | [статья](https://khorost.tech/go/go-orm-gorm-vs-go-jet/) |
| [`go/slog`](go/slog) | Структурированное логирование log/slog: хендлеры, группы, ContextHandler, бенчи | [статья](https://khorost.tech/go/go-slog/) |
| [`go/testing`](go/testing) | Тестирование в Go: ручные фейки, httptest, интеграционные тесты через testcontainers-go (Postgres + Redis), детектор гонок | 🔜 скоро |

## Java

| Пример | Описание | Статья |
|---|---|---|
| [`java/deep-dive`](java/deep-dive) | Java: глубокое погружение (JDK 25): virtual threads, Spring/Quarkus, GraalVM, JFR, Kafka EOS | [статья](https://khorost.tech/java/java-backend-and-containers-introduction/) |

## Performance

| Пример | Описание | Статья |
|---|---|---|
| [`performance/crypto-rsa-regression`](performance/crypto-rsa-regression) | Регрессия crypto/rsa в Go 1.20: одинаковые бенчмарки на шести версиях Go, просадка публичных verify/encrypt в 5–7 раз, benchstat и CI-гейт | [статья](https://khorost.tech/performance/go-crypto-rsa-regression/) |
| [`performance/highload-lowlatency`](performance/highload-lowlatency) | Highload под SLA < 300 мс: HAProxy L7 (h2c) + пул Go/Java-бэкендов, L4 vs L7 | [статья](https://khorost.tech/performance/latency-budget-and-transport/) |
| [`performance/probabilistic`](performance/probabilistic) | Вероятностные структуры: Bloom и родственники | [статья](https://khorost.tech/performance/bloom-filters-probabilistic-structures/) |
| [`performance/testcontainers-template-db`](performance/testcontainers-template-db) | Шаблонная база вместо контейнера на каждый тест: CREATE DATABASE ... TEMPLATE, замеры ×10 и ×36, границы приёма (права, FORCE, размер шаблона) | [статья](https://khorost.tech/performance/testcontainers-template-db/) |

## Infrastructure

| Пример | Описание | Статья |
|---|---|---|
| [`infrastructure/ansible`](infrastructure/ansible) | Деплой Docker Compose стека через Ansible: docker_compose_v2, vault | [статья](https://khorost.tech/infrastructure/ansible-docker-compose-deploy/) |
| [`infrastructure/proxmox`](infrastructure/proxmox) | Terraform для Proxmox: VM/LXC, cloud-init, for_each | [статья](https://khorost.tech/infrastructure/proxmox-terraform-vm-automation/) |
| [`infrastructure/terraform`](infrastructure/terraform) | Стык Terraform → Ansible: docker-хост + firewall + inventory | [статья](https://khorost.tech/infrastructure/terraform-docker-hosts-and-networks/) |

## Rust

| Пример | Описание | Статья |
|---|---|---|
| [`rust/docker-minimal`](rust/docker-minimal) | Rust: минимальные Docker-образы | [статья](https://khorost.tech/rust/rust-docker-minimal-images/) |
| [`rust/production`](rust/production) | Rust в production: каркас надёжного сервиса | [статья](https://khorost.tech/rust/rust-production-patterns/) |
| [`rust/testing`](rust/testing) | Тестирование в Rust: шесть приёмов на том же домене, что и стенд across-languages — сравнение с Go, Java и Python напрямую | [статья](https://khorost.tech/rust/rust-testing/) |
| [`rust/tokio`](rust/tokio) | Rust async: Tokio на практике | [статья](https://khorost.tech/rust/rust-async-tokio/) |
| [`rust/web-frameworks`](rust/web-frameworks) | Rust web-фреймворки: Axum vs Actix | [статья](https://khorost.tech/rust/rust-web-frameworks/) |

## Docker

| Пример | Описание | Статья |
|---|---|---|
| [`docker/rootless`](docker/rootless) | Rootful vs rootless Docker на живом стенде | [статья](https://khorost.tech/docker/rootless-docker/) |

## Zig

| Пример | Описание | Статья |
|---|---|---|
| [`zig/hello-comptime`](zig/hello-comptime) | Zig: comptime и позиционирование среди C/Rust/C++ | [статья](https://khorost.tech/zig/zig-positioning-among-c-rust-cpp/) |

## Security

| Пример | Описание | Статья |
|---|---|---|
| [`security/pixelsmash`](security/pixelsmash) | CVE-2026-8461 в декодерах FFmpeg: оборонительный стенд — аудит-скрипты и песочница для декодирования недоверенного видео | 🔜 скоро |

