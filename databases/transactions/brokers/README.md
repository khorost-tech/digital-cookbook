# Брокеры и стриминг: RabbitMQ, Kafka, outbox

Companion к статье [«Брокеры и стриминг: транзакции в очереди и логе — RabbitMQ и Kafka»](https://khorost.tech/databases/transactions-brokers-rabbitmq-kafka/)
(серия «Транзакции и изоляция»).

«Транзакция» в брокере — про гарантии доставки и атомарность публикации, не про изоляцию чтений.
Проверено на **RabbitMQ 4**, **Apache Kafka 3.9.0**, **PostgreSQL 18**.

| Пример | Механизм | Живьём |
|--------|----------|--------|
| RabbitMQ | publisher confirms (предпочтительнее дорогих AMQP-транзакций) + durable-очередь vs persistent-сообщение | ack=100 на обе очереди; после рестарта брокера persistent — **100 из 100**, transient — **0 из 100** |
| Kafka | транзакционный producer + read_committed (EOS) | aborted-транзакция невидима: committed=10, aborted=0 |
| outbox | PG-транзакция (бизнес + событие) + релей в Kafka | 20 событий атомарно с БД, релей опубликовал 20 |

## Запуск

```bash
docker compose up -d                      # rabbitmq :5673, kafka :9095, postgres :5441
```

> **Почему в compose у RabbitMQ стоит `user: rabbitmq` — не убирайте.** Без него в Docker на WSL
> контейнер выходит с кодом 1:
> `Error when reading /var/lib/rabbitmq/.erlang.cookie: eacces`.
>
> Механика: cookie имеет режим `0400` (owner-only), а брокер работает под `rabbitmq` (uid 999).
> Entrypoint образа заходит в ветку `[[ "$1" == rabbitmq* ]] && [ "$(id -u)" = 0 ]` → `chown` +
> `exec gosu rabbitmq`; в образе `/root/.erlang.cookie` — симлинк на
> `/var/lib/rabbitmq/.erlang.cookie`. Если drop привилегий не отрабатывает, Erlang стартует под
> root и создаёт cookie `root:root` прямо в volume — после чего брокер под 999 прочитать его не
> может. `user: rabbitmq` убирает root-фазу вовсе, поэтому cookie может создать только 999.
> Проверить владельца, если что-то пошло не так:
>
> ```bash
> docker run --rm --volumes-from tx-rabbit alpine stat -c '%u:%g' /var/lib/rabbitmq/.erlang.cookie
> # 999:999 — норма; 0:0 — этот запуск не взлетит
> ```
>
> **Проверено, что НЕ помогает** (если будете чинить иначе): именованный volume вместо
> анонимного (делает хуже — cookie стабильно `root:root`), `hostname:`,
> `RABBITMQ_ERLANG_COOKIE`, смена каталога проекта (drvfs ↔ ext4), пересоздание через
> `down -v`. **Чего делать нельзя:** bind-mount `/var/lib/rabbitmq` с Windows-пути
> (`/mnt/c`, `/mnt/g`, …) — drvfs не выражает owner-only права, Erlang откажется:
> `Cookie file ... must be accessible by owner only`.

### Go

```bash
cd go
go run . rabbit    # publisher confirms: 2 durable-очереди, ack=100 на каждую
go run . kafka     # EOS: read_committed consumer видит committed=10, aborted=0
go run . outbox    # 20 заказов+событий атомарно с БД; релей опубликовал 20 в Kafka
```

**Durable-очередь ≠ persistent-сообщение** — главный нюанс RabbitMQ, и его видно живьём.
Обе очереди объявлены `durable=true`, обе публикации получили `ack`, но переживают рестарт
только сообщения с `DeliveryMode: amqp.Persistent`:

```bash
cd go && go run . rabbit          # ack=100 в tx-demo (persistent) и tx-demo-transient (transient)

# Рестарт делать через stop → ждём фактической остановки → start.
# `docker compose restart` возвращается слишком рано: проверка готовности успевает пройти по
# ЕЩЁ НЕ упавшему процессу, и rabbit-verify опросит старый инстанс — transient «переживёт» рестарт,
# которого не было.
cd .. && docker compose stop rabbitmq
until [ "$(docker inspect -f '{{.State.Running}}' tx-rabbit)" = "false" ]; do sleep 1; done
docker compose start rabbitmq
# ping отвечает раньше, чем AMQP-листенер начинает принимать — ждём именно порт:
until docker exec tx-rabbit rabbitmq-diagnostics -q check_port_connectivity >/dev/null 2>&1; do sleep 2; done

cd go && go run . rabbit-verify   # tx-demo: 100 из 100 · tx-demo-transient: 0 из 100
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
docker compose down -v   # -v убирает и volume rabbit-data (сброс данных брокера)
```
