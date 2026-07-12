# 01-jetstream — JetStream: stream, consumer, KV

Демо к статье [«JetStream: как NATS получает persistence, streams и гарантии доставки»](https://khorost.tech/messaging/nats-jetstream-persistence-consumers/).

Один узел `nats-server` с включённым JetStream (`-js`, store dir `/data`, смонтирован как volume — данные переживают `docker compose down` без `-v`).

## Запуск

```bash
docker compose up -d
```

Проверка, что JetStream включён (monitoring endpoint):

```bash
curl -s localhost:8222/jsz | head -c 200
```

## nats CLI

Команды ниже выполняются `nats` CLI ([nats-io/natscli](https://github.com/nats-io/natscli)). Контекст на локальный сервер — как в `00-core`:

```bash
nats context save local --server localhost:4222 --select
```

## Stream

Создать stream `ORDERS`, забирающий всё дерево `orders.>`, с retention `limits` (поведение как у лога — сообщения живут по лимитам, а не по факту обработки):

```bash
nats stream add ORDERS --subjects "orders.>" --storage file --retention limits --defaults
```

Проверить, что stream создан:

```bash
nats stream ls
nats stream info ORDERS
```

## Pull consumer + ack

Создать durable pull-consumer `proc` с explicit ack:

```bash
nats consumer add ORDERS proc --pull --ack explicit --defaults
```

Опубликовать сообщение:

```bash
nats pub orders.new '{"id":1}'
```

Забрать сообщение pull-consumer'ом (сообщение нужно явно подтвердить, иначе через `AckWait` придёт повторно):

```bash
nats consumer next ORDERS proc
```

## Дедупликация (`Nats-Msg-Id`)

Публикация с заголовком `Nats-Msg-Id` — повторная публикация с тем же id в пределах окна дедупа (по умолчанию 2 минуты) не создаст второе сообщение в stream:

```bash
nats pub orders.new '{"id":1}' -H Nats-Msg-Id:1
nats pub orders.new '{"id":1}' -H Nats-Msg-Id:1   # дубликат — молча отброшен сервером

nats stream info ORDERS   # State.Messages не увеличился на второй публикации
```

## Workqueue retention: сообщение исчезает после ack

Отдельный stream с retention `workqueue` — сообщение живёт в stream до тех пор, пока его не заберёт и не подтвердит consumer, после чего удаляется (в отличие от `limits`, где сообщение остаётся до истечения лимитов независимо от ack):

```bash
nats stream add JOBS --subjects "jobs.>" --storage file --retention work --defaults
nats consumer add JOBS worker --pull --ack explicit --defaults

nats pub jobs.a '{"task":1}'
nats stream info JOBS       # State.Messages: 1

nats consumer next JOBS worker --ack
nats stream info JOBS       # State.Messages: 0 — сообщение удалено после ack
```

## KV

KV — надстройка над JetStream: bucket материализуется как stream `KV_<bucket>`.

```bash
nats kv add config
nats kv put config feature.flag true
nats kv get config feature.flag
```

## Остановка

```bash
docker compose down -v   # -v удаляет volume ./data вместе с данными stream'ов
```
