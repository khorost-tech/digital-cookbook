# Транзакции и изоляция — примеры и стенды

Companion-код к серии [«Транзакции и изоляция»](https://khorost.tech/databases/transactions-isolation-levels-anomalies/)
на khorost.tech. Транзакционность как сквозная тема — от аномалий и уровней изоляции до того, что
«транзакция» реально означает в разных классах хранилищ.

| Каталог | Статья | Что внутри |
|---------|--------|-----------|
| [`relational/`](relational) | [#2 PostgreSQL на практике](https://khorost.tech/databases/transactions-relational-postgres-practice/) | **Стенд №1**: lost update и write skew под конкуренцией на PostgreSQL, лечение и метрики (Go-нагрузчик + Java JDBC) |
| [`kv-document/`](kv-document) | [#3 KV и документные](https://khorost.tech/databases/transactions-kv-document-redis-mongo-scylla/) | Redis (`WATCH`-CAS) / MongoDB (multi-doc tx) / ScyllaDB (LWT) — что там «транзакция», Go + Java |
| [`brokers/`](brokers) | [#4 Брокеры и стриминг](https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/) | RabbitMQ confirms / Kafka EOS (транзакционный producer) / outbox (PG-tx → релей в Kafka), Go + Java |
| [`multistore/`](multistore) | [#5 Мульти-хранилищный стенд](https://khorost.tech/databases/transactions-multistore-benchmark-decision-map/) | **Стенд №2**: один сценарий (списание с инвариантом) по PostgreSQL/Redis/MongoDB/ScyllaDB одним Go-нагрузчиком + карта выбора |

Примеры на **Go** (основной) и **Java**. Каждый стенд самодостаточен: `docker compose up` + запуск.
Версии хранилищ пинуются в каждом каталоге; demo-креды — только для локальных стендов.
