# Полный список примеров

Все стенды digital-cookbook со ссылками на статьи. Навигация по категориям — в [README.md](README.md).

## Architecture

| Пример | Описание | Статья |
|---|---|---|
| [`architecture/distributed-config`](architecture/distributed-config) | etcd, ZooKeeper, Consul, Vault: watch, discovery, dynamic credentials | [статья](https://khorost.tech/architecture/distributed-configuration/) |
| [`architecture/event-coordination`](architecture/event-coordination) | Хореография vs оркестрация | [статья](https://khorost.tech/architecture/choreography-vs-orchestration/) |
| [`architecture/event-payload`](architecture/event-payload) | Notification vs event-carried state transfer | [статья](https://khorost.tech/architecture/event-notification-vs-state-transfer/) |
| [`architecture/event-sourcing`](architecture/event-sourcing) | Event Sourcing на практике: store, агрегаты, проекции | 🔜 скоро |
| [`architecture/idempotency`](architecture/idempotency) | Гарантии доставки и идемпотентность (effectively-once) | [статья](https://khorost.tech/architecture/delivery-guarantees-idempotency/) |
| [`architecture/temporal`](architecture/temporal) | Temporal: durable execution вглубь | [статья](https://khorost.tech/architecture/temporal-durable-workflows/) |

## Databases

| Пример | Описание | Статья |
|---|---|---|
| [`databases/clickhouse`](databases/clickhouse) | ClickHouse и аналитические БД: MergeTree, MV, кластер, S3 | [статья](https://khorost.tech/databases/clickhouse-when-olap/) |
| [`databases/db-indexes`](databases/db-indexes) | Индексы в БД: PostgreSQL, MongoDB, Tarantool | [статья](https://khorost.tech/databases/) |
| [`databases/opensearch`](databases/opensearch) | OpenSearch: кластер, индексы, ingest, полнотекст, ISM, семантика, Dashboards | [статья](https://khorost.tech/infrastructure/opensearch-cluster-ansible/) |
| [`databases/postgres`](databases/postgres) | Клиенты Go/Java/Rust к PostgreSQL: primary+replica+pgbouncer, failover | [статья](https://khorost.tech/databases/postgres-clients-reliability-go-java-rust/) |
| [`databases/redis/client-resilience`](databases/redis/client-resilience) | Клиенты Go/Java/Rust к Redis: Cluster/Sentinel, reconnect и failover | [статья](https://khorost.tech/databases/redis-clients-go-java-rust/) |
| [`databases/redis/deep-dive`](databases/redis/deep-dive) | Redis/Valkey: глубокое погружение (кодировки, event loop, персистентность, Cluster/Sentinel, память, streams/Lua, эксплуатация) | [статья](https://khorost.tech/databases/) |
| [`databases/scylladb`](databases/scylladb) | ScyllaDB: глубокое погружение (топология, компакция, LWT, драйверы) | 🔜 скоро |
| [`databases/transactions`](databases/transactions) | Транзакции и изоляция: реляционные, KV/документные, брокеры, мульти-хранилище | [статья](https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/) |

## Messaging

| Пример | Описание | Статья |
|---|---|---|
| [`messaging/kafka`](messaging/kafka) | Kafka: глубокое погружение (клиенты, ecosystem, MM2, ops, EOS) | [статья](https://khorost.tech/messaging/) |
| [`messaging/nats`](messaging/nats) | NATS 2.12: Core, JetStream, кластер, гео, безопасность, клиенты | [статья](https://khorost.tech/messaging/nats-core-subjects-request-reply/) |
| [`messaging/rabbitmq`](messaging/rabbitmq) | RabbitMQ 4.x: HA-кластер (quorum, DLQ, federation) и Streams | [статья](https://khorost.tech/messaging/rabbitmq-ha-cluster-quorum-failover/) |

## Go

| Пример | Описание | Статья |
|---|---|---|
| [`go/orm-gorm-vs-jet`](go/orm-gorm-vs-jet) | ORM в Go: GORM vs go-jet | [статья](https://khorost.tech/go/go-orm-gorm-vs-go-jet/) |
| [`go/slog`](go/slog) | Структурированное логирование log/slog: хендлеры, группы, ContextHandler, бенчи | [статья](https://khorost.tech/go/go-slog/) |

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
| [`rust/tokio`](rust/tokio) | Rust async: Tokio на практике | [статья](https://khorost.tech/rust/rust-async-tokio/) |
| [`rust/web-frameworks`](rust/web-frameworks) | Rust web-фреймворки: Axum vs Actix | [статья](https://khorost.tech/rust/rust-web-frameworks/) |

## Docker

| Пример | Описание | Статья |
|---|---|---|
| [`docker/rootless`](docker/rootless) | Rootful vs rootless Docker на живом стенде | [статья](https://khorost.tech/docker/rootless-docker/) |

