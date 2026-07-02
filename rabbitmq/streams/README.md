# Demo-стенд: RabbitMQ Streams

Рабочий стенд к статье [«Streams в RabbitMQ: Kafka-подобный лог и можно ли заменить Kafka»](https://khorost.tech/messaging/rabbitmq-streams-vs-kafka/) на khorost.tech.

Показывает в действии: append-only stream, publish confirmations и серверный offset tracking,
super streams (партиционирование) и доступ к тому же стриму через обычный AMQP 0.9.1.

---

## Содержание

1. [Проверено на](#проверено-на)
2. [Требования](#требования)
3. [Запуск брокера](#запуск-брокера)
4. [Демонстрации](#демонстрации)
5. [Ожидаемый вывод](#ожидаемый-вывод)
6. [Очистка](#очистка)

---

## Проверено на

- RabbitMQ **4.3.2** (образ `rabbitmq:4.3-management`), плагины `rabbitmq_stream` + `rabbitmq_stream_management`
- Go stream-клиент `github.com/rabbitmq/rabbitmq-stream-go-client` **v1.8.1**
- AMQP-клиент `github.com/rabbitmq/amqp091-go` **v1.12.0**

Поведение streams меняется между версиями RabbitMQ — стенд пиновать к указанным версиям.

## Требования

- Docker + Docker Compose
- Go 1.23+

## Запуск брокера

```bash
docker compose up -d
# дождаться healthy:
docker inspect -f '{{.State.Health.Status}}' rabbit-stream
```

Порты: `5552` (stream protocol), `5672` (AMQP), `15672` (management UI, guest/guest).
`stream.advertised_host = localhost` в `rabbitmq/rabbitmq.conf` — иначе stream-клиент
получит внутренний hostname контейнера и не подключится.

## Демонстрации

```bash
cd go
go mod tidy

# 1) native stream: producer с publish confirmations + consumer с First-offset и offset tracking
go run ./stream

# 2) super stream: 3 партиции, hash-роутинг по ключу, super consumer со всех партиций
go run ./superstream

# 3) тот же stream через AMQP 0.9.1: x-queue-type=stream + обязательные qos и x-stream-offset
go run ./amqp
```

## Ожидаемый вывод

```
# go run ./stream
produced+confirmed 100
consumed 100 from First; сохранённый server-side offset = 99

# go run ./superstream
partitions (3): [orders-0 orders-1 orders-2]
produced 60 в super stream
super-consumed all 60 across 3 partitions

# go run ./amqp
AMQP: consumed 20 from x-queue-type=stream (x-stream-offset=first)
```

Распределение по партициям super stream (60 сообщений, 10 ключей, 3 партиции) неравномерно —
хеш ключей ложится по партициям как ~12/30/18. Это нормально: равномерность даёт разнообразие
ключей, а не число сообщений. Проверить:

```bash
docker exec rabbit-stream rabbitmqctl list_queues name type messages | grep orders
```

## Очистка

```bash
docker compose down
```
