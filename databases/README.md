# Databases — примеры

Реляционные, документные, колоночные и KV-хранилища, поиск, индексы, транзакции, WAL.

| Стенд | Описание | Статья |
|---|---|---|
| [`clickhouse/`](clickhouse/) | ClickHouse и аналитические БД: MergeTree, MV, кластер, S3 | [статья](https://khorost.tech/databases/clickhouse-when-olap/) |
| [`db-indexes/`](db-indexes/) | Индексы в БД: PostgreSQL, MongoDB, Tarantool | [статья](https://khorost.tech/databases/) |
| [`mongodb/`](mongodb/) | MongoDB: глубокое погружение (модель, индексы, репликация, шардирование) | 🔜 скоро |
| [`opensearch/`](opensearch/) | OpenSearch: кластер, индексы, ingest, полнотекст, ISM, семантика, Dashboards | [статья](https://khorost.tech/infrastructure/opensearch-cluster-ansible/) |
| [`postgres/`](postgres/) | Клиенты Go/Java/Rust к PostgreSQL: primary+replica+pgbouncer, failover | [статья](https://khorost.tech/databases/postgres-clients-reliability-go-java-rust/) |
| [`redis/client-resilience/`](redis/client-resilience/) | Клиенты Go/Java/Rust к Redis: Cluster/Sentinel, reconnect и failover | [статья](https://khorost.tech/databases/redis-clients-go-java-rust/) |
| [`redis/deep-dive/`](redis/deep-dive/) | Redis/Valkey: глубокое погружение (кодировки, event loop, персистентность, Cluster/Sentinel, память, streams/Lua, эксплуатация) | [статья](https://khorost.tech/databases/) |
| [`scylladb/`](scylladb/) | ScyllaDB: глубокое погружение (топология, компакция, LWT, драйверы) | [статья](https://khorost.tech/databases/) |
| [`transactions/`](transactions/) | Транзакции и изоляция: реляционные, KV/документные, брокеры, мульти-хранилище | [статья](https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/) |
| [`identifiers/`](identifiers/) | Идентификаторы: локальность и bloat UUIDv4/UUIDv7/bigint в PostgreSQL, распределение в Mongo/Scylla, генерация в Go/Java/Rust | 🔜 скоро |

---

Навигация: [все категории](../README.md) · [полный список примеров](../INDEX.md)
