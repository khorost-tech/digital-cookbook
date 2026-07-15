# Брокеры и стриминг: RabbitMQ, Kafka, outbox

Companion к статье [«Брокеры и стриминг: транзакции в очереди и логе — RabbitMQ и Kafka»](https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/)
(серия «Транзакции и изоляция»).

«Транзакция» в брокере — про гарантии доставки и атомарность публикации, не про изоляцию чтений.
Проверено на **RabbitMQ 4**, **Apache Kafka 3.9.0**, **PostgreSQL 18**.

| Пример | Механизм | Живьём |
|--------|----------|--------|
| RabbitMQ | publisher confirms (предпочтительнее дорогих AMQP-транзакций) | 100 публикаций, ack=100 |
| Kafka | транзакционный producer + read_committed (EOS) | aborted-транзакция невидима: committed=10, aborted=0 |
| outbox | PG-транзакция (бизнес + событие) + релей в Kafka | 20 событий атомарно с БД, релей опубликовал 20 |

## Запуск

```bash
docker compose up -d                      # rabbitmq :5673, kafka :9095, postgres :5441
```

> **RabbitMQ на Docker Desktop/Windows.** На некоторых сборках Docker Desktop контейнер RabbitMQ
> из compose падает с `Error when reading /var/lib/rabbitmq/.erlang.cookie: eacces` (особенность
> монтирования анонимного volume образа). На Linux/обычном Docker всё работает. Обходной путь —
> запустить RabbitMQ отдельно: `docker run -d --name tx-rabbit -p 5673:5672 rabbitmq:4-management`.

### Go

```bash
cd go
go run . rabbit    # publisher confirms: 100 публикаций, ack=100
go run . kafka     # EOS: read_committed consumer видит committed=10, aborted=0
go run . outbox    # 20 заказов+событий атомарно с БД; релей опубликовал 20 в Kafka
```

### Java (Kafka — каноничный транзакционный API на JVM)

```bash
cd java
KAFKA=localhost:9095 mvn -q compile exec:java   # EOS: committed=10, aborted=0
```

## Что где

| Путь | Назначение |
|------|-----------|
| `docker-compose.yml` | RabbitMQ 4 / Kafka 3.9 (KRaft) / PostgreSQL 18 |
| `go/` | Go: amqp091-go / franz-go / pgx |
| `java/` | Java: kafka-clients (транзакционный producer + read_committed) |

## Demo-only

Локальный стенд без TLS/аутентификации сверх дефолтной, данные синтетические.

## Teardown

```bash
docker compose down
docker rm -f tx-rabbit   # если запускали RabbitMQ отдельно
```
