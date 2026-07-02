# clients/go — nats.go: Core, Request/Reply, JetStream, KV

Демо к статье [«NATS из Go: nats.go, Core и JetStream паттерны»](https://khorost.tech/messaging/nats-go-client-patterns/) — шестая статья серии «Погружение в NATS».

Модуль `nats-cookbook-go`, зависимость `github.com/nats-io/nats.go v1.52.0` (актуальная на момент написания статьи). Требуется Go **1.25+** — это не выбор статьи, а фактическое ограничение самого `nats.go` v1.52.0 (его собственный `go.mod` объявляет `go 1.25.0`; более старый Go модуль не соберёт).

JetStream-примеры используют **новый** пакет `github.com/nats-io/nats.go/jetstream`, а не устаревший `nc.JetStream()` (пакет `nats`, тип `JetStreamContext`) — старый API всё ещё работает, но не развивается и не показан здесь намеренно.

## Примеры

| Каталог | Что показывает |
|---------|-----------------|
| [`cmd/core-pubsub`](cmd/core-pubsub) | Core NATS: async subscribe, sync subscribe (`SubscribeSync`/`NextMsg`), queue group (`QueueSubscribe`) |
| [`cmd/request-reply`](cmd/request-reply) | Responder в queue group + requester с таймаутом через `context` |
| [`cmd/jetstream-pull`](cmd/jetstream-pull) | Пакет `jetstream`: stream, pull-consumer, `Fetch`, ручной `Ack`, publish с `MsgId` (дедупликация) |
| [`cmd/kv`](cmd/kv) | KV: `Put`/`Get`/`Watch` |

Общий код подключения — [`internal/natsconn`](internal/natsconn): опции reconnect (`MaxReconnects(-1)`, `ReconnectWait`), коллбэки `DisconnectErrHandler`/`ReconnectHandler`/`ClosedHandler`, `Shutdown()` на базе `Drain()` вместо `Close()`.

## Запуск

Примеры подключаются к `localhost:4222` — это либо [`nats/02-cluster`](../../02-cluster) (нода `n1`), либо [`nats/01-jetstream`](../../01-jetstream) (JetStream уже нужен для `jetstream-pull` и `kv`). `core-pubsub` и `request-reply` не требуют JetStream и подойдут и с [`nats/00-core`](../../00-core).

```bash
cd nats/01-jetstream    # или nats/02-cluster
docker compose up -d
sleep 3                 # дать серверу подняться

cd ../clients/go
go mod tidy
go build ./...
go vet ./...

go run ./cmd/core-pubsub
go run ./cmd/request-reply
go run ./cmd/jetstream-pull
go run ./cmd/kv
```

Все примеры — однократные демо-прогоны: подключаются, публикуют/потребляют несколько сообщений, печатают в лог и завершаются через `Drain()`. Это сделано намеренно, чтобы `go run` было достаточно без Ctrl+C — в продакшн-сервисе `Drain()`/`Shutdown()` вызывается по сигналу `SIGTERM`, а не по завершении `main()`.

Остановка стенда:

```bash
cd ../../01-jetstream    # или 02-cluster
docker compose down -v
```

## Проверка dedup вручную

`jetstream-pull` публикует одно сообщение дважды с одинаковым `Nats-Msg-Id` (`order-1`) и печатает `duplicate=true` на второй публикации. Проверить со стороны сервера:

```bash
nats stream info ORDERS   # State.Messages не увеличился на второй публикации
```

## Документация

- [nats.go — Go Packages](https://pkg.go.dev/github.com/nats-io/nats.go)
- [jetstream — Go Packages](https://pkg.go.dev/github.com/nats-io/nats.go/jetstream)
- [nats-io/nats.go на GitHub](https://github.com/nats-io/nats.go)
