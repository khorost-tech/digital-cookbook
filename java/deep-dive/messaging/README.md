# messaging

Стенд к статье №11 (java-deep-dive): **Kafka — producer/consumer, consumer groups,
exactly-once, обработка ошибок** на живом Kafka (`apache/kafka:4.3.0`, KRaft
single-node, из общего `docker/compose.yml`).

## Сценарии (реальные прогоны, `Main`, режим `all`)

### (1) Producer/consumer: базовая доставка

Топик `demo-basic` (3 партиции), 20 сообщений producer -> consumer:

```
[producer] всего отправлено: 20
[consumer] всего получено: 20
АССЕРТ OK: отправлено=20, получено=20
```

### (2) Consumer groups: реальный ребаланс

Топик `demo-groups` (4 партиции), 40 сообщений засеяны заранее. Сначала
подключается `consumer-1` (в одиночку получает все 4 партиции), затем
`consumer-2` — это триггерит ребаланс, залогированный через
`ConsumerRebalanceListener` (revoke у обоих старых назначений, новый assign):

```
[rebalance] consumer-1: assigned [demo-groups-0, demo-groups-1, demo-groups-2, demo-groups-3]
[rebalance] consumer-1: revoked  [demo-groups-0, demo-groups-1, demo-groups-2, demo-groups-3]
[rebalance] consumer-1: assigned [demo-groups-0, demo-groups-1]
[rebalance] consumer-2: assigned [demo-groups-2, demo-groups-3]
АССЕРТ OK: партиции распределены без пересечений (2+2=4 из 4), сообщений дочитано=40 (>=40)
```

`consumer-1` успел дочитать все 40 сообщений ДО ребаланса (партиции были
засеяны заранее и он был единственным в группе) — валидный результат, ассерт
проверяет распределение партиций и суммарное количество прочитанного, а не то,
кто именно прочитал.

### (3) Exactly-once: транзакционный producer + read_committed consumer

Топик `demo-eos` (3 партиции). Транзакционный producer
(`transactional.id=messaging-eos-producer`, `enable.idempotence=true`):

- батч A (5 сообщений) — штатный `commitTransaction()`.
- батч B, попытка 1 (5 сообщений) — симуляция сбоя обработки в середине батча
  -> `abortTransaction()`.
- батч B, попытка 2 (те же 5 сообщений) — штатный `commitTransaction()`.

Consumer с `isolation.level=read_committed`:

```
физически отправлено записей: 15 (включая абортнутый батч)
логически подтверждено: 10
увидел read_committed-консьюмер: 10
АССЕРТ OK: read_committed увидел=10 (абортнутый батч невидим, дублей нет)
```

Абортнутые 5 записей физически лежат в логе партиций (control-record ABORT),
но `read_committed`-консьюмер их полностью отфильтровывает — при повторе после
сбоя дублей нет.

### (4) Обработка ошибок: retry + dead-letter topic

Топик `demo-errors` (3 партиции) + `demo-errors-dlt` (1 партиция). 15
сообщений, каждое 5-е — "ядовитое" (маркер `POISON`, обработка всегда падает).
На каждое ядовитое — 3 попытки с бэкоффом (150мс × номер попытки), затем
отправка в DLT (заголовки `x-original-topic/partition/offset/error`) и коммит
оффсета — партиция не блокируется:

```
попытка 1/3 провалилась для offset=1 value=payload-4-POISON: ...
попытка 2/3 провалилась для offset=1 value=payload-4-POISON: ...
попытка 3/3 провалилась для offset=1 value=payload-4-POISON: ...
offset=1 value=payload-4-POISON -> DLT после 3 неудачных попыток
...
итог: обработано успешно=12, в DLT=3 (перепроверено чтением DLT=3)
АССЕРТ OK: успешно обработано=12, в DLT после 3 ретраев=3 (перепроверено=3)
```

## Ловушка: NOT_LEADER_OR_FOLLOWER сразу после создания топика

При создании топика через `AdminClient` и немедленной отправке в него (сценарий
consumer groups) продюсер иногда получает `NotLeaderOrFollowerException` на
первой попытке — метаданные о свежесозданном топике ещё не разошлись между
клиентом и брокером в момент первого запроса. Это штатное и ожидаемое
поведение (single-node broker, задержка в доли секунды): `KafkaProducer`
автоматически ретраит с обновлением метаданных (WARN в логе, не ошибка) и
сообщения доставляются. В проде с этим сталкиваются реже (топики обычно уже
существуют к моменту продьюсинга), но сама причина и авто-retry поведение —
универсальны для любого кластера.

## Версии

- `kafka-clients`: `4.3.0` (пин parent POM, совпадает с версией образа брокера)
- Kafka broker: `apache/kafka:4.3.0`, KRaft single-node (`docker/compose.yml`)

## Сборка и прогон

Хостового Maven нет — сборка через Docker; Kafka из compose гоняем ВНУТРИ
контейнера на сети compose (`kafka:9092` — внутренний PLAINTEXT-листенер,
не host-порт `9096`, который может быть заблокирован файрволом хоста):

```bash
cd java-deep-dive
docker compose -f docker/compose.yml up -d kafka
# дождаться healthy: docker inspect --format='{{.State.Health.Status}}' jdd-kafka

MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(pwd)/..:/app" -v "$HOME/.m2:/root/.m2" \
  -w /app/java-deep-dive maven:3.9-eclipse-temurin-25 \
  mvn -q -pl messaging -am package -DskipTests

NET="$(docker network ls --format '{{.Name}}' | grep -m1 '_default$' | grep -i docker || echo docker_default)"
MSYS_NO_PATHCONV=1 docker run --rm --network "$NET" \
  -e KAFKA_BOOTSTRAP="kafka:9092" \
  -v "$(pwd)/messaging/target:/app" eclipse-temurin:25-jdk \
  java -jar /app/messaging.jar all

docker compose -f docker/compose.yml down
```

Режимы (первый аргумент `Main`): `all` (по умолчанию) | `basic` | `groups` |
`eos` | `errors`.
