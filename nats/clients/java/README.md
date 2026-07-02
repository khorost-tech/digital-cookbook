# clients/java — jnats: Core, Request/Reply, JetStream, KV

Демо к статье [«NATS из Java: jnats, Core и JetStream паттерны»](https://khorost.tech/messaging/nats-java-client-patterns/) — седьмая статья серии «Погружение в NATS». Зеркалит [`clients/go`](../go) — те же четыре паттерна, другой язык.

Модуль `nats-cookbook-java`, зависимость `io.nats:jnats 2.25.3` (актуальный релиз на момент написания статьи, 2026-05-07). Требуется Java **17+** (сборка настроена на `maven.compiler.release=17`).

Примеры используют «упрощённый» (simplified) JetStream API — `JetStream.getStreamContext`/`StreamContext.getConsumerContext`/`ConsumerContext` — а не более старый `JetStreamSubscription`/`PullSubscribeOptions`, который тоже есть в библиотеке, но требует больше ручной настройки подписки.

## Примеры

| Класс | Что показывает |
|-------|-----------------|
| [`CorePubSub`](src/main/java/tech/khorost/nats/CorePubSub.java) | Core NATS: async subscribe через `Dispatcher`, sync subscribe (`Connection.subscribe` + `Subscription.nextMessage`), queue group (`Dispatcher.subscribe(subject, queue)`) |
| [`RequestReply`](src/main/java/tech/khorost/nats/RequestReply.java) | Responder в queue group + requester с явным таймаутом через `CompletableFuture.get(timeout, unit)` |
| [`JetStreamPull`](src/main/java/tech/khorost/nats/JetStreamPull.java) | Stream и durable pull-consumer через `JetStreamManagement`, pull через `ConsumerContext.fetchMessages`, ручной `Message.ack()`, publish с `messageId` (дедупликация) |
| [`Kv`](src/main/java/tech/khorost/nats/Kv.java) | KV: `put`/`get`/`watch` (`KeyValueWatcher` — callback, а не канал, как в Go) |

Общий код подключения — [`NatsConn`](src/main/java/tech/khorost/nats/NatsConn.java): опции reconnect (`maxReconnects(-1)`, `reconnectWait`), `ConnectionListener`/`ErrorListener` вместо трёх раздельных Go-хендлеров, адрес сервера переопределяется переменной окружения `NATS_URL`.

## Запуск

Примеры подключаются к `localhost:4222` — это либо [`nats/02-cluster`](../../02-cluster) (нода `n1`), либо [`nats/01-jetstream`](../../01-jetstream) (JetStream уже нужен для `JetStreamPull` и `Kv`). `CorePubSub` и `RequestReply` не требуют JetStream и подойдут и с [`nats/00-core`](../../00-core).

```bash
cd nats/01-jetstream    # или nats/02-cluster
docker compose up -d
sleep 3                 # дать серверу подняться

cd ../clients/java
mvn -q compile

mvn -q exec:java -Dexec.mainClass=tech.khorost.nats.CorePubSub
mvn -q exec:java -Dexec.mainClass=tech.khorost.nats.RequestReply
mvn -q exec:java -Dexec.mainClass=tech.khorost.nats.JetStreamPull
mvn -q exec:java -Dexec.mainClass=tech.khorost.nats.Kv
```

Если `mvn` не установлен локально — тот же прогон через Docker:

```bash
docker run --rm \
  --network 01-jetstream_default \
  -v "$(pwd):/workspace" -w /workspace \
  -v maven-repo:/root/.m2 \
  -e NATS_URL=nats://01-jetstream-nats-1:4222 \
  maven:3.9-eclipse-temurin-21 \
  mvn -q compile exec:java -Dexec.mainClass=tech.khorost.nats.CorePubSub
```

(имя сети и контейнера — `<каталог-стенда>_default` и `<каталог-стенда>-nats-1`, `docker network ls` / `docker ps` подскажут точные значения; `NATS_URL` переопределяет адрес по умолчанию `nats://127.0.0.1:4222` — см. `NatsConn`.)

Все примеры — однократные демо-прогоны: подключаются, публикуют/потребляют несколько сообщений, печатают в лог и завершаются через `drain()`. Это сделано намеренно, чтобы `exec:java` было достаточно без Ctrl+C — в продакшн-сервисе `drain()` вызывается по сигналу `SIGTERM`, а не по завершении `main()`.

Остановка стенда:

```bash
cd ../../01-jetstream    # или 02-cluster
docker compose down -v
```

## Проверка dedup вручную

`JetStreamPull` публикует одно сообщение дважды с одинаковым `messageId` (`order-1`) и печатает `duplicate=true` на второй публикации. Проверить со стороны сервера:

```bash
nats stream info ORDERS   # State.Messages не увеличился на второй публикации
```

Повторный запуск `JetStreamPull` против того же стенда (без пересоздания `docker compose`) снова покажет `duplicate=true` на обеих публикациях, пока не истечёт окно дедупа стрима (по умолчанию 2 минуты) — это ожидаемо, не баг демо.

## Документация

- [nats-io/nats.java на GitHub](https://github.com/nats-io/nats.java)
- [jnats — Javadoc (javadoc.io)](https://javadoc.io/doc/io.nats/jnats/latest/index.html)
- [io.nats:jnats — Maven Central](https://central.sonatype.com/artifact/io.nats/jnats)
- [NATS docs](https://docs.nats.io/)
