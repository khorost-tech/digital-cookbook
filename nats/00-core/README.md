# 00-core — Core NATS: pub/sub, request/reply, queue groups

Демо к статье [«NATS для тех, кто знает Kafka и RabbitMQ: subjects, Core и request/reply»](https://khorost.tech/messaging/nats-core-subjects-request-reply/).

Один узел `nats-server`, без JetStream — только Core NATS: subjects, pub/sub, request/reply, queue groups.

## Запуск

```bash
docker compose up -d
```

Проверка, что сервер поднялся (monitoring endpoint):

```bash
curl -s localhost:8222/varz | head -c 200
```

## nats CLI

Команды ниже выполняются `nats` CLI — он ставится отдельно от сервера ([nats-io/natscli](https://github.com/nats-io/natscli)):

```bash
# macOS
brew install nats-io/nats-tools/nats

# или бинарник с GitHub Releases: https://github.com/nats-io/natscli/releases
```

Настроить контекст на локальный сервер (один раз):

```bash
nats context save local --server localhost:4222 --select
```

## Pub/Sub

Терминал 1 — подписка на дерево `orders.>`:

```bash
nats sub "orders.>"
```

Терминал 2 — публикация:

```bash
nats pub orders.eu.new '{"id":1}'
```

Сообщение придёт в терминал 1. Если подписчика нет в момент публикации — сообщение теряется безвозвратно: Core NATS ничего не хранит (at-most-once by design).

## Request/Reply

Терминал 1 — сервис, отвечающий на запросы (join queue group `NATS-RPLY-22` автоматически):

```bash
nats reply service.time '{{Time}}'
```

Терминал 2 — запрос:

```bash
nats request service.time ''
```

`nats reply` подставит текущее время вместо `{{Time}}` и отправит ответ в inbox-subject, который `nats request` создал автоматически.

## Queue groups

Терминал 1 и терминал 2 — два подписчика в одной queue group `workers`:

```bash
nats sub --queue workers jobs.*
```

Терминал 3 — публикация нескольких сообщений подряд:

```bash
nats pub jobs.a x
nats pub jobs.a x
nats pub jobs.a x
```

Каждое сообщение получит только один из двух подписчиков — сервер распределяет их по очереди (competing consumers), а не рассылает всем.

## Остановка

```bash
docker compose down
```
